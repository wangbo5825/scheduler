package scheduler

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
)

func TestHTTPModeSendsRequest(t *testing.T) {
	var gotMethod, gotBody, gotAuth, gotContentType string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	s := &Scheduler{
		Mode:   ModeHTTP,
		URL:    server.URL,
		Method: http.MethodPost,
		Headers: http.Header{
			"Authorization": {"Bearer secret"},
			"Content-Type":  {"application/json"},
		},
		Body: `{"source":"caddy"}`,
	}

	status, _, err := s.doHTTP(context.Background())
	if err != nil {
		t.Fatalf("doHTTP failed: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("expected status 200, got %d", status)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("expected POST, got %s", gotMethod)
	}
	if gotAuth != "Bearer secret" {
		t.Fatalf("expected Authorization header, got %q", gotAuth)
	}
	if gotContentType != "application/json" {
		t.Fatalf("expected Content-Type header, got %q", gotContentType)
	}
	if gotBody != `{"source":"caddy"}` {
		t.Fatalf("unexpected body %q", gotBody)
	}
}

func TestHTTPModeStatusHandling(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/created":
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	s := &Scheduler{Mode: ModeHTTP, URL: server.URL + "/created", ExpectStatus: http.StatusCreated}
	if _, _, err := s.doHTTP(context.Background()); err != nil {
		t.Fatalf("expected 201 to succeed: %v", err)
	}

	s.ExpectStatus = http.StatusOK
	if _, _, err := s.doHTTP(context.Background()); err == nil {
		t.Fatal("expected status mismatch to fail")
	}

	s.ExpectStatus = 0
	s.URL = server.URL + "/failure"
	if _, _, err := s.doHTTP(context.Background()); err == nil {
		t.Fatal("expected non-2xx without expect_status to fail")
	}
}

func TestNextRunIntervalGrid(t *testing.T) {
	base := time.Date(2026, 9, 6, 12, 34, 56, 123456789, time.Local)

	s := &Scheduler{Interval: caddy.Duration(time.Minute)}
	next := s.nextRun(base)
	want := time.Date(2026, 9, 6, 12, 35, 0, 0, time.Local)
	if !next.Equal(want) {
		t.Fatalf("nextRun(1m) = %s, want %s", next, want)
	}

	s.Interval = caddy.Duration(30 * time.Second)
	next = s.nextRun(base)
	want = time.Date(2026, 9, 6, 12, 35, 0, 0, time.Local)
	if !next.Equal(want) {
		t.Fatalf("nextRun(30s) = %s, want %s", next, want)
	}

	s.Interval = caddy.Duration(5 * time.Minute)
	next = s.nextRun(base)
	want = time.Date(2026, 9, 6, 12, 35, 0, 0, time.Local)
	if !next.Equal(want) {
		t.Fatalf("nextRun(5m) = %s, want %s", next, want)
	}
}

func TestNextRunCronSchedule(t *testing.T) {
	s := &Scheduler{Schedule: "*/5 * * * *"}
	if err := s.setDefaults(slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("setDefaults failed: %v", err)
	}

	base := time.Date(2026, 9, 6, 12, 34, 56, 0, time.Local)
	next := s.nextRun(base)
	want := time.Date(2026, 9, 6, 12, 35, 0, 0, time.Local)
	if !next.Equal(want) {
		t.Fatalf("cron nextRun = %s, want %s", next, want)
	}
}

func TestModeInferenceAndValidation(t *testing.T) {
	s := &Scheduler{URL: "http://127.0.0.1:8080/tick"}
	if err := s.setDefaults(slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("setDefaults with url failed: %v", err)
	}
	if s.Mode != ModeHTTP {
		t.Fatalf("expected inferred mode http, got %s", s.Mode)
	}
	if s.Method != http.MethodGet {
		t.Fatalf("expected default method GET, got %s", s.Method)
	}
	if s.Interval != caddy.Duration(time.Minute) {
		t.Fatalf("expected default interval 1m, got %s", time.Duration(s.Interval))
	}

	s = &Scheduler{}
	if err := s.setDefaults(slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("setDefaults default failed: %v", err)
	}
	if s.Mode != ModeCommand {
		t.Fatalf("expected inferred mode command, got %s", s.Mode)
	}
	if strings.Join(s.Command, " ") != "php artisan schedule:run" {
		t.Fatalf("unexpected default command %v", s.Command)
	}

	s = &Scheduler{Mode: ModeHTTP}
	if err := s.setDefaults(slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Fatal("expected error for http mode without url")
	}
}
