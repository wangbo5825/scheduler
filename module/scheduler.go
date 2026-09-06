package scheduler

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/robfig/cron/v3"
)

const (
	OverlapAllow = "allow"
	OverlapSkip  = "skip"

	ModeCommand = "command"
	ModeHTTP    = "http"

	defaultInterval = time.Minute
)

func init() {
	caddy.RegisterModule(new(Scheduler))
	httpcaddyfile.RegisterGlobalOption("pogo_scheduler", parseGlobalOption)
}

// Scheduler is a Caddy/FrankenPHP app that periodically runs a job. A job is
// either an external command (the historical behavior) or a native HTTP(S)
// request (the fork's primary addition).
//
// The HTTP mode deliberately avoids spawning system processes from inside the
// web server. Scheduled work is pushed over the existing request pipeline of
// Caddy/FrankenPHP, where it can be handled by a long-running PHP worker or any
// other HTTP service. That keeps the web server free of shell access and makes
// each tick cheap: no process startup per run, and no host command surface.
type Scheduler struct {
	// Mode selects the job type. Supported values are "command" and "http".
	// When empty it is inferred: "http" when URL is set, otherwise "command".
	Mode string `json:"mode,omitempty"`

	// Command mode options (kept compatible with upstream pogo_scheduler).
	Command []string `json:"command,omitempty"`
	Dir     string   `json:"dir,omitempty"`

	// HTTP mode options.
	URL                string      `json:"url,omitempty"`
	Method             string      `json:"method,omitempty"`
	Headers            http.Header `json:"headers,omitempty"`
	Body               string      `json:"body,omitempty"`
	ExpectStatus       int         `json:"expect_status,omitempty"`
	InsecureSkipVerify bool        `json:"insecure_skip_verify,omitempty"`

	// Scheduling options.
	// Interval (default 1m) is the period between ticks on a wall-clock-aligned
	// grid. Schedule, when set, is a cron expression (standard 5-field syntax,
	// descriptors such as "@every 5m", and the optional "CRON_TZ=Zone" prefix)
	// and takes precedence over Interval.
	Interval caddy.Duration `json:"interval,omitempty"`
	Schedule string         `json:"schedule,omitempty"`

	// Common execution options.
	Timeout       caddy.Duration `json:"timeout,omitempty"`
	Overlap       string         `json:"overlap,omitempty"`
	ShutdownGrace caddy.Duration `json:"shutdown_grace,omitempty"`

	logger       *slog.Logger
	cronSchedule cron.Schedule
	httpClient   *http.Client
	scheduleCtx  context.Context
	cancelLoop   context.CancelFunc
	runCtx       context.Context
	cancelRuns   context.CancelFunc
	mu           sync.Mutex
	stopping     bool
	active       bool
	wg           sync.WaitGroup
	cancelRunsMu sync.Mutex
}

func (*Scheduler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "pogo_scheduler",
		New: func() caddy.Module { return new(Scheduler) },
	}
}

func (s *Scheduler) Provision(ctx caddy.Context) error {
	return s.setDefaults(ctx.Slogger())
}

func (s *Scheduler) setDefaults(logger *slog.Logger) error {
	if logger != nil {
		s.logger = logger
	}
	if s.logger == nil {
		s.logger = slog.Default()
	}

	if s.Mode == "" {
		if s.URL != "" {
			s.Mode = ModeHTTP
		} else {
			s.Mode = ModeCommand
		}
	}

	switch s.Mode {
	case ModeCommand:
		if s.URL != "" {
			return fmt.Errorf("url %q cannot be combined with mode %q", s.URL, ModeCommand)
		}
		if len(s.Command) == 0 {
			s.Command = []string{"php", "artisan", "schedule:run"}
		}
	case ModeHTTP:
		if len(s.Command) > 0 {
			return fmt.Errorf("command %v cannot be combined with mode %q", s.Command, ModeHTTP)
		}
		if s.URL == "" {
			return errors.New("mode \"http\" requires a url")
		}
		parsed, err := url.Parse(s.URL)
		if err != nil {
			return fmt.Errorf("invalid url %q: %w", s.URL, err)
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return fmt.Errorf("invalid url %q: scheme must be http or https", s.URL)
		}
		if parsed.Host == "" {
			return fmt.Errorf("invalid url %q: host is required", s.URL)
		}
		if s.Method == "" {
			s.Method = http.MethodGet
		}
		s.Method = strings.ToUpper(s.Method)
		if _, err := http.NewRequest(s.Method, s.URL, nil); err != nil {
			return fmt.Errorf("invalid method %q: %w", s.Method, err)
		}
		if s.ExpectStatus < 0 {
			return fmt.Errorf("invalid expect_status %d: must not be negative", s.ExpectStatus)
		}
		if s.InsecureSkipVerify {
			s.logger.Warn("insecure_skip_verify is enabled; TLS certificates will not be verified",
				slog.String("url", s.URL),
			)
		}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		if s.InsecureSkipVerify {
			transport.TLSClientConfig = &tls.Config{
				InsecureSkipVerify: true,
				MinVersion:         tls.VersionTLS12,
			}
		}
		s.httpClient = &http.Client{Transport: transport}
	default:
		return fmt.Errorf("invalid mode %q: expected %q or %q", s.Mode, ModeCommand, ModeHTTP)
	}

	if s.Interval <= 0 {
		s.Interval = caddy.Duration(defaultInterval)
	}
	if s.Interval < caddy.Duration(time.Second) {
		return fmt.Errorf("invalid interval %s: must be at least 1s", time.Duration(s.Interval))
	}
	if s.Schedule != "" {
		schedule, err := cron.ParseStandard(s.Schedule)
		if err != nil {
			return fmt.Errorf("invalid schedule %q: %w", s.Schedule, err)
		}
		s.cronSchedule = schedule
	}

	if s.Timeout <= 0 {
		s.Timeout = caddy.Duration(5 * time.Minute)
	}
	if s.Overlap == "" {
		s.Overlap = OverlapAllow
	}
	if s.Overlap != OverlapSkip && s.Overlap != OverlapAllow {
		return fmt.Errorf("invalid overlap %q: expected %q or %q", s.Overlap, OverlapSkip, OverlapAllow)
	}
	if s.ShutdownGrace <= 0 {
		s.ShutdownGrace = caddy.Duration(30 * time.Second)
	}

	return nil
}

func (s *Scheduler) Start() error {
	if err := s.setDefaults(nil); err != nil {
		return err
	}

	s.scheduleCtx, s.cancelLoop = context.WithCancel(context.Background())
	s.runCtx, s.cancelRuns = context.WithCancel(context.Background())

	s.mu.Lock()
	s.stopping = false
	s.active = false
	s.wg.Add(1)
	s.mu.Unlock()

	go s.loop()

	attrs := []any{
		slog.String("mode", s.Mode),
		slog.String("overlap", s.Overlap),
		slog.Duration("interval", time.Duration(s.Interval)),
	}
	if s.Schedule != "" {
		attrs = append(attrs, slog.String("schedule", s.Schedule))
	}
	switch s.Mode {
	case ModeHTTP:
		attrs = append(attrs,
			slog.String("method", s.Method),
			slog.String("url", s.URL),
		)
	default:
		attrs = append(attrs,
			slog.Any("command", s.Command),
			slog.String("dir", s.Dir),
		)
	}
	s.logger.Info("scheduler started", attrs...)
	return nil
}

func (s *Scheduler) Stop() error {
	s.mu.Lock()
	s.stopping = true
	if s.cancelLoop != nil {
		s.cancelLoop()
	}
	s.mu.Unlock()

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	grace := time.Duration(s.ShutdownGrace)
	timer := time.NewTimer(grace)
	select {
	case <-done:
		timer.Stop()
	case <-timer.C:
		s.logger.Warn("scheduler shutdown grace elapsed, cancelling active runs",
			slog.Duration("shutdown_grace", grace),
		)
		s.cancelRunsNow()
		<-done
	}

	s.logger.Info("scheduler stopped")
	return nil
}

func (s *Scheduler) loop() {
	defer s.wg.Done()

	for {
		now := time.Now()
		next := s.nextRun(now)
		timer := time.NewTimer(next.Sub(now))

		select {
		case <-s.scheduleCtx.Done():
			timer.Stop()
			return
		case <-timer.C:
			s.logger.Info("scheduler tick", slog.Time("scheduled_at", next))
			s.startRun()
		}
	}
}

func (s *Scheduler) startRun() {
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return
	}
	if s.Overlap == OverlapSkip && s.active {
		s.mu.Unlock()
		s.logger.Warn("scheduler skipped: previous run still active")
		return
	}
	if s.Overlap == OverlapSkip {
		s.active = true
	}
	s.wg.Add(1)
	s.mu.Unlock()

	go s.run()
}

func (s *Scheduler) run() {
	defer s.wg.Done()
	if s.Overlap == OverlapSkip {
		defer func() {
			s.mu.Lock()
			s.active = false
			s.mu.Unlock()
		}()
	}

	ctx, cancel := context.WithTimeout(s.runCtx, time.Duration(s.Timeout))
	defer cancel()

	switch s.Mode {
	case ModeHTTP:
		s.runHTTP(ctx)
	default:
		s.runCommand(ctx)
	}
}

func (s *Scheduler) cancelRunsNow() {
	s.cancelRunsMu.Lock()
	defer s.cancelRunsMu.Unlock()
	if s.cancelRuns != nil {
		s.cancelRuns()
	}
}

// UnmarshalCaddyfile sets up the module from Caddyfile tokens.
// Syntax:
//
//	pogo_scheduler {
//	    mode command|http
//	    command php artisan schedule:run
//	    dir /var/www/html
//	    url https://localhost/artisan/schedule
//	    method POST
//	    header Authorization "Bearer secret"
//	    header Content-Type application/json
//	    body "{\"source\":\"caddy\"}"
//	    expect_status 200
//	    insecure_skip_verify false
//	    interval 5m
//	    schedule "*/5 * * * *"
//	    timeout 2m
//	    overlap allow
//	    shutdown_grace 30s
//	}
func (s *Scheduler) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	for d.Next() {
		for d.NextBlock(0) {
			switch d.Val() {
			case "mode":
				if !d.NextArg() {
					return d.ArgErr()
				}
				s.Mode = d.Val()
			case "command":
				s.Command = d.RemainingArgs()
				if len(s.Command) == 0 {
					return d.ArgErr()
				}
			case "dir":
				if !d.NextArg() {
					return d.ArgErr()
				}
				s.Dir = d.Val()
			case "url":
				if !d.NextArg() {
					return d.ArgErr()
				}
				s.URL = d.Val()
			case "method":
				if !d.NextArg() {
					return d.ArgErr()
				}
				s.Method = d.Val()
			case "header":
				if !d.NextArg() {
					return d.ArgErr()
				}
				name := d.Val()
				values := d.RemainingArgs()
				if len(values) == 0 {
					return d.ArgErr()
				}
				if s.Headers == nil {
					s.Headers = make(http.Header)
				}
				for _, value := range values {
					s.Headers.Add(name, value)
				}
			case "body":
				parts := d.RemainingArgs()
				if len(parts) == 0 {
					return d.ArgErr()
				}
				s.Body = strings.Join(parts, " ")
			case "expect_status":
				if !d.NextArg() {
					return d.ArgErr()
				}
				status, err := strconv.Atoi(d.Val())
				if err != nil {
					return d.Errf("invalid expect_status %q: %v", d.Val(), err)
				}
				s.ExpectStatus = status
			case "insecure_skip_verify":
				if !d.NextArg() {
					return d.ArgErr()
				}
				skip, err := strconv.ParseBool(d.Val())
				if err != nil {
					return d.Errf("invalid insecure_skip_verify %q: %v", d.Val(), err)
				}
				s.InsecureSkipVerify = skip
			case "interval":
				if !d.NextArg() {
					return d.ArgErr()
				}
				dur, err := caddy.ParseDuration(d.Val())
				if err != nil {
					return d.Errf("invalid duration %s: %v", d.Val(), err)
				}
				s.Interval = caddy.Duration(dur)
			case "schedule":
				if !d.NextArg() {
					return d.ArgErr()
				}
				s.Schedule = d.Val()
			case "timeout":
				if !d.NextArg() {
					return d.ArgErr()
				}
				dur, err := caddy.ParseDuration(d.Val())
				if err != nil {
					return d.Errf("invalid duration %s: %v", d.Val(), err)
				}
				s.Timeout = caddy.Duration(dur)
			case "overlap":
				if !d.NextArg() {
					return d.ArgErr()
				}
				s.Overlap = d.Val()
				if s.Overlap != OverlapSkip && s.Overlap != OverlapAllow {
					return d.Errf("invalid overlap %q: expected %q or %q", s.Overlap, OverlapSkip, OverlapAllow)
				}
			case "shutdown_grace":
				if !d.NextArg() {
					return d.ArgErr()
				}
				dur, err := caddy.ParseDuration(d.Val())
				if err != nil {
					return d.Errf("invalid duration %s: %v", d.Val(), err)
				}
				s.ShutdownGrace = caddy.Duration(dur)
			default:
				return d.Errf("unrecognized subdirective: %s", d.Val())
			}
		}
	}
	return nil
}

func parseGlobalOption(d *caddyfile.Dispenser, _ any) (any, error) {
	app := &Scheduler{}
	if err := app.UnmarshalCaddyfile(d); err != nil {
		return nil, err
	}

	return httpcaddyfile.App{
		Name:  "pogo_scheduler",
		Value: caddyconfig.JSON(app, nil),
	}, nil
}

var (
	_ caddy.Module          = (*Scheduler)(nil)
	_ caddy.App             = (*Scheduler)(nil)
	_ caddy.Provisioner     = (*Scheduler)(nil)
	_ caddyfile.Unmarshaler = (*Scheduler)(nil)
)
