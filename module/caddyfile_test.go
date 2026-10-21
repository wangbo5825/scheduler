package scheduler

import (
	"strings"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
)

func TestUnmarshalCaddyfileHTTPMode(t *testing.T) {
	input := `
pogo_scheduler {
	mode            http
	url             http://127.0.0.1:8080/tick
	method          POST
	header          Authorization "Bearer secret"
	header          Content-Type application/json
	body            "{\"source\":\"caddy\"}"
	expect_status   204
	interval        5m
	schedule        "@every 1h"
	timeout         30s
	overlap         skip
	shutdown_grace  10s
}
`
	d := caddyfile.NewTestDispenser(input)
	s := new(Scheduler)
	if err := s.UnmarshalCaddyfile(d); err != nil {
		t.Fatalf("UnmarshalCaddyfile failed: %v", err)
	}

	if s.Mode != ModeHTTP {
		t.Fatalf("expected mode http, got %q", s.Mode)
	}
	if s.URL != "http://127.0.0.1:8080/tick" {
		t.Fatalf("unexpected url %q", s.URL)
	}
	if s.Method != "POST" {
		t.Fatalf("unexpected method %q", s.Method)
	}
	if got := s.Headers.Get("Authorization"); got != "Bearer secret" {
		t.Fatalf("unexpected Authorization header %q", got)
	}
	if got := s.Headers.Get("Content-Type"); got != "application/json" {
		t.Fatalf("unexpected Content-Type header %q", got)
	}
	if s.Body != `{"source":"caddy"}` {
		t.Fatalf("unexpected body %q", s.Body)
	}
	if s.ExpectStatus != 204 {
		t.Fatalf("unexpected expect_status %d", s.ExpectStatus)
	}
	if s.Interval != caddy.Duration(5*time.Minute) {
		t.Fatalf("unexpected interval %s", time.Duration(s.Interval))
	}
	if s.Schedule != "@every 1h" {
		t.Fatalf("unexpected schedule %q", s.Schedule)
	}
	if s.Overlap != OverlapSkip {
		t.Fatalf("unexpected overlap %q", s.Overlap)
	}
}

func TestUnmarshalCaddyfileMultipleJobs(t *testing.T) {
	input := `
pogo_scheduler {
	shutdown_grace 45s

	job backup {
		mode     http
		url      http://127.0.0.1:8080/backup
		method   POST
		schedule "0 3 * * *"
		timeout  10m
	}

	job cleanup {
		command  php artisan cleanup
		dir      /var/www/html
		interval 5m
		overlap  skip
	}
}
`
	d := caddyfile.NewTestDispenser(input)
	s := new(Scheduler)
	if err := s.UnmarshalCaddyfile(d); err != nil {
		t.Fatalf("UnmarshalCaddyfile failed: %v", err)
	}

	if len(s.Jobs) != 2 {
		t.Fatalf("expected two jobs, got %d", len(s.Jobs))
	}
	backup := s.Jobs[0]
	if backup.Name != "backup" || backup.Mode != ModeHTTP {
		t.Fatalf("unexpected first job: %+v", backup)
	}
	if backup.URL != "http://127.0.0.1:8080/backup" || backup.Method != "POST" {
		t.Fatalf("unexpected backup request: %+v", backup)
	}
	if backup.Schedule != "0 3 * * *" || backup.Timeout != caddy.Duration(10*time.Minute) {
		t.Fatalf("unexpected backup schedule: %+v", backup)
	}

	cleanup := s.Jobs[1]
	if cleanup.Name != "cleanup" || cleanup.Mode != "" {
		t.Fatalf("unexpected second job: %+v", cleanup)
	}
	if strings.Join(cleanup.Command, " ") != "php artisan cleanup" {
		t.Fatalf("unexpected cleanup command %v", cleanup.Command)
	}
	if cleanup.Interval != caddy.Duration(5*time.Minute) || cleanup.Overlap != OverlapSkip {
		t.Fatalf("unexpected cleanup schedule: %+v", cleanup)
	}
	if s.ShutdownGrace != caddy.Duration(45*time.Second) {
		t.Fatalf("unexpected shutdown grace %s", time.Duration(s.ShutdownGrace))
	}
	if s.Job.Mode != "" || s.Job.URL != "" || len(s.Job.Command) != 0 {
		t.Fatalf("legacy job fields should remain empty: mode=%q url=%q command=%v",
			s.Job.Mode, s.Job.URL, s.Job.Command)
	}
}
