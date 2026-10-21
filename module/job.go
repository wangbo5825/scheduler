package scheduler

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/robfig/cron/v3"
)

// Job is one named scheduled action. A job is either a command or an HTTP(S)
// request and has its own schedule, timeout, and overlap policy.
type Job struct {
	Name string `json:"name,omitempty"`

	// Mode selects the job type. Supported values are "command" and "http".
	// When empty it is inferred: "http" when URL is set, otherwise "command".
	Mode string `json:"mode,omitempty"`

	// Command mode options.
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

	// Execution options.
	Timeout caddy.Duration `json:"timeout,omitempty"`
	Overlap string         `json:"overlap,omitempty"`

	logger       *slog.Logger
	cronSchedule cron.Schedule
	httpClient   *http.Client
	mu           sync.Mutex
	active       bool
}

func (j *Job) setDefaults(parent *slog.Logger) error {
	if j.Name == "" {
		j.Name = "default"
	}
	if parent == nil {
		parent = slog.Default()
	}
	j.logger = parent.With(slog.String("job", j.Name))

	if j.Mode == "" {
		if j.URL != "" {
			j.Mode = ModeHTTP
		} else {
			j.Mode = ModeCommand
		}
	}

	switch j.Mode {
	case ModeCommand:
		if j.URL != "" {
			return fmt.Errorf("url %q cannot be combined with mode %q", j.URL, ModeCommand)
		}
		if len(j.Command) == 0 {
			j.Command = []string{"php", "artisan", "schedule:run"}
		}

	case ModeHTTP:
		if len(j.Command) > 0 {
			return fmt.Errorf("command %v cannot be combined with mode %q", j.Command, ModeHTTP)
		}
		if j.URL == "" {
			return errors.New("mode \"http\" requires a url")
		}
		parsed, err := url.Parse(j.URL)
		if err != nil {
			return fmt.Errorf("invalid url %q: %w", j.URL, err)
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return fmt.Errorf("invalid url %q: scheme must be http or https", j.URL)
		}
		if parsed.Host == "" {
			return fmt.Errorf("invalid url %q: host is required", j.URL)
		}
		if j.Method == "" {
			j.Method = http.MethodGet
		}
		j.Method = strings.ToUpper(j.Method)
		if _, err := http.NewRequest(j.Method, j.URL, nil); err != nil {
			return fmt.Errorf("invalid method %q: %w", j.Method, err)
		}
		if j.ExpectStatus < 0 {
			return fmt.Errorf("invalid expect_status %d: must not be negative", j.ExpectStatus)
		}
		if j.InsecureSkipVerify {
			j.logger.Warn("insecure_skip_verify is enabled; TLS certificates will not be verified",
				slog.String("url", j.URL),
			)
		}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		if j.InsecureSkipVerify {
			transport.TLSClientConfig = &tls.Config{
				InsecureSkipVerify: true,
				MinVersion:         tls.VersionTLS12,
			}
		}
		j.httpClient = &http.Client{Transport: transport}

	default:
		return fmt.Errorf("invalid mode %q: expected %q or %q", j.Mode, ModeCommand, ModeHTTP)
	}

	if j.Interval <= 0 {
		j.Interval = caddy.Duration(defaultInterval)
	}
	if j.Interval < caddy.Duration(time.Second) {
		return fmt.Errorf("invalid interval %s: must be at least 1s", time.Duration(j.Interval))
	}
	if j.Schedule != "" {
		schedule, err := cron.ParseStandard(j.Schedule)
		if err != nil {
			return fmt.Errorf("invalid schedule %q: %w", j.Schedule, err)
		}
		j.cronSchedule = schedule
	}

	if j.Timeout <= 0 {
		j.Timeout = caddy.Duration(5 * time.Minute)
	}
	if j.Overlap == "" {
		j.Overlap = OverlapAllow
	}
	if j.Overlap != OverlapSkip && j.Overlap != OverlapAllow {
		return fmt.Errorf("invalid overlap %q: expected %q or %q", j.Overlap, OverlapSkip, OverlapAllow)
	}

	return nil
}

func (j *Job) logConfig() {
	attrs := []any{
		slog.String("mode", j.Mode),
		slog.String("overlap", j.Overlap),
		slog.Duration("interval", time.Duration(j.Interval)),
		slog.Duration("timeout", time.Duration(j.Timeout)),
	}
	if j.Schedule != "" {
		attrs = append(attrs, slog.String("schedule", j.Schedule))
	}
	switch j.Mode {
	case ModeHTTP:
		attrs = append(attrs,
			slog.String("method", j.Method),
			slog.String("url", j.URL),
		)
	default:
		attrs = append(attrs,
			slog.Any("command", j.Command),
			slog.String("dir", j.Dir),
		)
	}
	j.logger.Info("scheduler job configured", attrs...)
}
