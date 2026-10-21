package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
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

// Scheduler is a Caddy/FrankenPHP app that periodically runs one or more
// named jobs. Each job can execute an external command or perform a native
// HTTP(S) request.
//
// The HTTP mode deliberately avoids spawning system processes from inside the
// web server. Scheduled work is pushed over the existing request pipeline of
// Caddy/FrankenPHP, where it can be handled by a long-running PHP worker or any
// other HTTP service. That keeps the web server free of shell access and makes
// each tick cheap: no process startup per run, and no host command surface.
type Scheduler struct {
	// Jobs defines named jobs. When empty, the legacy single-job fields
	// embedded below (mode, command, url, interval, schedule, ...) are used.
	Jobs []*Job `json:"jobs,omitempty"`

	// Job keeps the original flat single-job configuration backward-compatible.
	Job

	// ShutdownGrace is the maximum time active runs across all jobs may
	// finish during shutdown before they are cancelled.
	ShutdownGrace caddy.Duration `json:"shutdown_grace,omitempty"`

	logger       *slog.Logger
	jobList      []*Job
	scheduleCtx  context.Context
	cancelLoop   context.CancelFunc
	runCtx       context.Context
	cancelRuns   context.CancelFunc
	mu           sync.Mutex
	stopping     bool
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
	if s.ShutdownGrace <= 0 {
		s.ShutdownGrace = caddy.Duration(30 * time.Second)
	}

	if len(s.Jobs) == 0 {
		if s.Job.Name == "" {
			s.Job.Name = "default"
		}
		if err := s.Job.setDefaults(s.logger); err != nil {
			return fmt.Errorf("job %q: %w", s.Job.Name, err)
		}
		s.jobList = []*Job{&s.Job}
		return nil
	}

	if err := s.validateLegacyJobUnused(); err != nil {
		return err
	}

	seen := make(map[string]struct{}, len(s.Jobs))
	jobList := make([]*Job, 0, len(s.Jobs))
	for i, job := range s.Jobs {
		if job == nil {
			return fmt.Errorf("job %d: definition is null", i)
		}
		job.Name = strings.TrimSpace(job.Name)
		if job.Name == "" {
			return fmt.Errorf("job %d: name is required", i)
		}
		if _, exists := seen[job.Name]; exists {
			return fmt.Errorf("duplicate job name %q", job.Name)
		}
		seen[job.Name] = struct{}{}

		if err := job.setDefaults(s.logger); err != nil {
			return fmt.Errorf("job %q: %w", job.Name, err)
		}
		jobList = append(jobList, job)
	}
	s.jobList = jobList
	return nil
}

func (s *Scheduler) validateLegacyJobUnused() error {
	job := &s.Job
	switch {
	case job.Name != "":
		return errors.New("top-level job options cannot be combined with jobs; move them into a job block")
	case job.Mode != "":
		return errors.New("top-level mode cannot be combined with jobs; move it into a job block")
	case len(job.Command) > 0:
		return errors.New("top-level command cannot be combined with jobs; move it into a job block")
	case job.Dir != "":
		return errors.New("top-level dir cannot be combined with jobs; move it into a job block")
	case job.URL != "":
		return errors.New("top-level url cannot be combined with jobs; move it into a job block")
	case job.Method != "":
		return errors.New("top-level method cannot be combined with jobs; move it into a job block")
	case len(job.Headers) > 0:
		return errors.New("top-level header cannot be combined with jobs; move it into a job block")
	case job.Body != "":
		return errors.New("top-level body cannot be combined with jobs; move it into a job block")
	case job.ExpectStatus != 0:
		return errors.New("top-level expect_status cannot be combined with jobs; move it into a job block")
	case job.InsecureSkipVerify:
		return errors.New("top-level insecure_skip_verify cannot be combined with jobs; move it into a job block")
	case job.Interval != 0:
		return errors.New("top-level interval cannot be combined with jobs; move it into a job block")
	case job.Schedule != "":
		return errors.New("top-level schedule cannot be combined with jobs; move it into a job block")
	case job.Timeout != 0:
		return errors.New("top-level timeout cannot be combined with jobs; move it into a job block")
	case job.Overlap != "":
		return errors.New("top-level overlap cannot be combined with jobs; move it into a job block")
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
	for _, job := range s.jobList {
		job.mu.Lock()
		job.active = false
		job.mu.Unlock()
	}
	s.wg.Add(len(s.jobList))
	s.mu.Unlock()

	for _, job := range s.jobList {
		go s.loopJob(job)
	}

	s.logger.Info("scheduler started",
		slog.Int("jobs", len(s.jobList)),
		slog.Duration("shutdown_grace", time.Duration(s.ShutdownGrace)),
	)
	for _, job := range s.jobList {
		job.logConfig()
	}
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

	s.logger.Info("scheduler stopped", slog.Int("jobs", len(s.jobList)))
	return nil
}

func (s *Scheduler) loopJob(job *Job) {
	defer s.wg.Done()

	for {
		now := time.Now()
		next := job.nextRun(now)
		timer := time.NewTimer(next.Sub(now))

		select {
		case <-s.scheduleCtx.Done():
			timer.Stop()
			return
		case <-timer.C:
			job.logger.Info("scheduler tick", slog.Time("scheduled_at", next))
			s.startRun(job)
		}
	}
}

func (s *Scheduler) startRun(job *Job) {
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return
	}

	job.mu.Lock()
	if job.Overlap == OverlapSkip && job.active {
		job.mu.Unlock()
		s.mu.Unlock()
		job.logger.Warn("scheduler skipped: previous run still active")
		return
	}
	if job.Overlap == OverlapSkip {
		job.active = true
	}
	s.wg.Add(1)
	job.mu.Unlock()
	s.mu.Unlock()

	go s.runJob(job)
}

func (s *Scheduler) runJob(job *Job) {
	defer s.wg.Done()
	if job.Overlap == OverlapSkip {
		defer func() {
			job.mu.Lock()
			job.active = false
			job.mu.Unlock()
		}()
	}

	ctx, cancel := context.WithTimeout(s.runCtx, time.Duration(job.Timeout))
	defer cancel()

	job.execute(ctx)
}

func (s *Scheduler) cancelRunsNow() {
	s.cancelRunsMu.Lock()
	defer s.cancelRunsMu.Unlock()
	if s.cancelRuns != nil {
		s.cancelRuns()
	}
}

// UnmarshalCaddyfile sets up the module from Caddyfile tokens.
//
// Legacy single-job syntax is supported unchanged:
//
//	pogo_scheduler {
//	    command php artisan schedule:run
//	    interval 1m
//	}
//
// Multiple named jobs use job blocks:
//
//	pogo_scheduler {
//	    shutdown_grace 30s
//
//	    job backup {
//	        mode     http
//	        url      http://127.0.0.1/internal/backup
//	        schedule "0 3 * * *"
//	        timeout  10m
//	    }
//
//	    job cleanup {
//	        mode     command
//	        command  php artisan cleanup
//	        dir      /var/www/html
//	        interval 5m
//	        overlap  skip
//	    }
//	}
func (s *Scheduler) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	for d.Next() {
		for nesting := d.Nesting(); d.NextBlock(nesting); {
			switch d.Val() {
			case "job":
				if !d.NextArg() {
					return d.ArgErr()
				}
				name := d.Val()
				if d.NextArg() {
					return d.ArgErr()
				}

				job := &Job{Name: name}
				jobNesting := d.Nesting()
				for d.NextBlock(jobNesting) {
					handled, err := parseJobDirective(d, job)
					if err != nil {
						return err
					}
					if !handled {
						return d.Errf("unrecognized job subdirective: %s", d.Val())
					}
				}
				s.Jobs = append(s.Jobs, job)

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
				handled, err := parseJobDirective(d, &s.Job)
				if err != nil {
					return err
				}
				if !handled {
					return d.Errf("unrecognized subdirective: %s", d.Val())
				}
			}
		}
	}
	return nil
}

// parseJobDirective parses one job-level directive. The dispenser must be
// positioned on the directive token itself. The bool reports whether the
// directive was recognized.
func parseJobDirective(d *caddyfile.Dispenser, job *Job) (bool, error) {
	switch d.Val() {
	case "name":
		value, err := singleArg(d)
		if err != nil {
			return true, err
		}
		if job.Name != "" {
			return true, d.Err("job name already specified")
		}
		job.Name = value
	case "mode":
		value, err := singleArg(d)
		if err != nil {
			return true, err
		}
		job.Mode = value
	case "command":
		job.Command = d.RemainingArgs()
		if len(job.Command) == 0 {
			return true, d.ArgErr()
		}
	case "dir":
		value, err := singleArg(d)
		if err != nil {
			return true, err
		}
		job.Dir = value
	case "url":
		value, err := singleArg(d)
		if err != nil {
			return true, err
		}
		job.URL = value
	case "method":
		value, err := singleArg(d)
		if err != nil {
			return true, err
		}
		job.Method = value
	case "header":
		if !d.NextArg() {
			return true, d.ArgErr()
		}
		name := d.Val()
		values := d.RemainingArgs()
		if len(values) == 0 {
			return true, d.ArgErr()
		}
		if job.Headers == nil {
			job.Headers = make(http.Header)
		}
		for _, value := range values {
			job.Headers.Add(name, value)
		}
	case "body":
		parts := d.RemainingArgs()
		if len(parts) == 0 {
			return true, d.ArgErr()
		}
		job.Body = strings.Join(parts, " ")
	case "expect_status":
		value, err := singleArg(d)
		if err != nil {
			return true, err
		}
		status, err := strconv.Atoi(value)
		if err != nil {
			return true, d.Errf("invalid expect_status %q: %v", value, err)
		}
		job.ExpectStatus = status
	case "insecure_skip_verify":
		value, err := singleArg(d)
		if err != nil {
			return true, err
		}
		skip, err := strconv.ParseBool(value)
		if err != nil {
			return true, d.Errf("invalid insecure_skip_verify %q: %v", value, err)
		}
		job.InsecureSkipVerify = skip
	case "interval":
		value, err := singleArg(d)
		if err != nil {
			return true, err
		}
		dur, err := caddy.ParseDuration(value)
		if err != nil {
			return true, d.Errf("invalid duration %s: %v", value, err)
		}
		job.Interval = caddy.Duration(dur)
	case "schedule":
		value, err := singleArg(d)
		if err != nil {
			return true, err
		}
		job.Schedule = value
	case "timeout":
		value, err := singleArg(d)
		if err != nil {
			return true, err
		}
		dur, err := caddy.ParseDuration(value)
		if err != nil {
			return true, d.Errf("invalid duration %s: %v", value, err)
		}
		job.Timeout = caddy.Duration(dur)
	case "overlap":
		value, err := singleArg(d)
		if err != nil {
			return true, err
		}
		if value != OverlapSkip && value != OverlapAllow {
			return true, d.Errf("invalid overlap %q: expected %q or %q", value, OverlapSkip, OverlapAllow)
		}
		job.Overlap = value
	default:
		return false, nil
	}
	return true, nil
}

func singleArg(d *caddyfile.Dispenser) (string, error) {
	if !d.NextArg() {
		return "", d.ArgErr()
	}
	value := d.Val()
	if d.NextArg() {
		return "", d.ArgErr()
	}
	return value, nil
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
