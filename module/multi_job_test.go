package scheduler

import (
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestSetDefaultsMultipleJobs(t *testing.T) {
	s := &Scheduler{
		Jobs: []*Job{
			{
				Name:     "tick",
				URL:      "http://127.0.0.1:8080/tick",
				Interval: caddy.Duration(30 * time.Second),
			},
			{
				Name:    "cleanup",
				Command: []string{"php", "artisan", "cleanup"},
				Overlap: OverlapSkip,
			},
		},
		logger: discardLogger(),
	}

	if err := s.setDefaults(nil); err != nil {
		t.Fatalf("setDefaults failed: %v", err)
	}
	if len(s.jobList) != 2 {
		t.Fatalf("expected two jobs, got %d", len(s.jobList))
	}

	tick := s.jobList[0]
	if tick.Mode != ModeHTTP || tick.Method != "GET" {
		t.Fatalf("unexpected HTTP defaults: %+v", tick)
	}
	if tick.Interval != caddy.Duration(30*time.Second) || tick.Timeout != caddy.Duration(5*time.Minute) {
		t.Fatalf("unexpected HTTP timing defaults: %+v", tick)
	}
	if tick.Overlap != OverlapAllow {
		t.Fatalf("unexpected HTTP overlap default: %q", tick.Overlap)
	}

	cleanup := s.jobList[1]
	if cleanup.Mode != ModeCommand || cleanup.Interval != caddy.Duration(time.Minute) {
		t.Fatalf("unexpected command defaults: %+v", cleanup)
	}
	if cleanup.Overlap != OverlapSkip {
		t.Fatalf("unexpected command overlap: %q", cleanup.Overlap)
	}
}

func TestSetDefaultsLegacySingleJob(t *testing.T) {
	s := &Scheduler{
		Job: Job{
			Command: []string{"php", "artisan", "schedule:run"},
		},
		logger: discardLogger(),
	}

	if err := s.setDefaults(nil); err != nil {
		t.Fatalf("setDefaults failed: %v", err)
	}
	if len(s.jobList) != 1 {
		t.Fatalf("expected one legacy job, got %d", len(s.jobList))
	}
	if s.jobList[0].Name != "default" {
		t.Fatalf("unexpected legacy job name %q", s.jobList[0].Name)
	}
}

func TestMultipleJobValidation(t *testing.T) {
	tests := []struct {
		name string
		job  *Scheduler
		want string
	}{
		{
			name: "missing name",
			job: &Scheduler{
				Jobs:   []*Job{{Command: []string{"true"}}},
				logger: discardLogger(),
			},
			want: "name is required",
		},
		{
			name: "duplicate name",
			job: &Scheduler{
				Jobs: []*Job{
					{Name: "same", Command: []string{"true"}},
					{Name: "same", Command: []string{"true"}},
				},
				logger: discardLogger(),
			},
			want: "duplicate job name",
		},
		{
			name: "legacy option mixed with jobs",
			job: func() *Scheduler {
				s := &Scheduler{
					Jobs:   []*Job{{Name: "one", Command: []string{"true"}}},
					logger: discardLogger(),
				}
				s.Job.URL = "http://127.0.0.1:8080/legacy"
				return s
			}(),
			want: "top-level url cannot be combined with jobs",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.job.setDefaults(nil)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected error containing %q, got %v", test.want, err)
			}
		})
	}
}

func TestSchedulerJSONMultiJobAndLegacyCompatibility(t *testing.T) {
	multi := &Scheduler{
		Jobs: []*Job{
			{Name: "one", URL: "http://127.0.0.1:8080/one"},
			{Name: "two", Command: []string{"php", "artisan", "two"}},
		},
	}
	data, err := json.Marshal(multi)
	if err != nil {
		t.Fatalf("marshal multi-job config: %v", err)
	}
	var multiJSON map[string]any
	if err := json.Unmarshal(data, &multiJSON); err != nil {
		t.Fatalf("unmarshal multi-job config: %v", err)
	}
	if _, ok := multiJSON["jobs"]; !ok {
		t.Fatalf("expected jobs field, got %s", data)
	}
	if _, ok := multiJSON["Job"]; ok {
		t.Fatalf("embedded job must be inlined, got %s", data)
	}

	legacy := &Scheduler{Job: Job{Command: []string{"php", "artisan", "schedule:run"}}}
	data, err = json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal legacy config: %v", err)
	}
	var legacyJSON map[string]any
	if err := json.Unmarshal(data, &legacyJSON); err != nil {
		t.Fatalf("unmarshal legacy config: %v", err)
	}
	if _, ok := legacyJSON["command"]; !ok {
		t.Fatalf("legacy command must remain top-level, got %s", data)
	}
	if _, ok := legacyJSON["jobs"]; ok {
		t.Fatalf("legacy config should not emit jobs, got %s", data)
	}

	var decodedMulti Scheduler
	if err := json.Unmarshal([]byte(`{"jobs":[{"name":"one","url":"http://127.0.0.1:8080/one"},{"name":"two","command":["php","artisan","two"]}]}`), &decodedMulti); err != nil {
		t.Fatalf("decode multi-job config: %v", err)
	}
	if len(decodedMulti.Jobs) != 2 || decodedMulti.Jobs[0].Name != "one" {
		t.Fatalf("unexpected decoded multi-job config: %+v", decodedMulti.Jobs)
	}

	var decodedLegacy Scheduler
	if err := json.Unmarshal([]byte(`{"command":["php","artisan","schedule:run"]}`), &decodedLegacy); err != nil {
		t.Fatalf("decode legacy config: %v", err)
	}
	if len(decodedLegacy.Job.Command) != 3 || decodedLegacy.Job.Command[1] != "artisan" {
		t.Fatalf("unexpected decoded legacy command: %v", decodedLegacy.Job.Command)
	}
}
