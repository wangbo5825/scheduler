package scheduler

import (
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
)

func TestDefaultOverlapAllowsConcurrentRuns(t *testing.T) {
	logPath := tempLogPath(t)

	s := newTestScheduler(t, helperCommand("sleep-write", logPath, "150ms"))
	s.Overlap = ""
	s.ShutdownGrace = caddy.Duration(time.Second)
	requireStart(t, s)

	s.startRun(s.jobList[0])
	waitForContent(t, logPath, "START", 3*time.Second)
	s.startRun(s.jobList[0])

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	content := readFile(t, logPath)
	if got := strings.Count(content, "START"); got != 2 {
		t.Fatalf("expected two command starts, got %d in %q", got, content)
	}
	if got := strings.Count(content, "DONE"); got != 2 {
		t.Fatalf("expected two command completions, got %d in %q", got, content)
	}
}

func TestOverlapSkipSkipsActiveRun(t *testing.T) {
	logPath := tempLogPath(t)

	s := newTestScheduler(t, helperCommand("sleep-write", logPath, "150ms"))
	s.Overlap = OverlapSkip
	s.ShutdownGrace = caddy.Duration(time.Second)
	requireStart(t, s)

	s.startRun(s.jobList[0])
	waitForContent(t, logPath, "START", 3*time.Second)
	s.startRun(s.jobList[0])

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	content := readFile(t, logPath)
	if got := strings.Count(content, "START"); got != 1 {
		t.Fatalf("expected exactly one command start, got %d in %q", got, content)
	}
	if got := strings.Count(content, "DONE"); got != 1 {
		t.Fatalf("expected exactly one command completion, got %d in %q", got, content)
	}
}

func TestOverlapAllowStartsConcurrentRuns(t *testing.T) {
	logPath := tempLogPath(t)

	s := newTestScheduler(t, helperCommand("sleep-write", logPath, "150ms"))
	s.Overlap = OverlapAllow
	s.ShutdownGrace = caddy.Duration(time.Second)
	requireStart(t, s)

	s.startRun(s.jobList[0])
	s.startRun(s.jobList[0])

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	content := readFile(t, logPath)
	if got := strings.Count(content, "START"); got != 2 {
		t.Fatalf("expected two command starts, got %d in %q", got, content)
	}
	if got := strings.Count(content, "DONE"); got != 2 {
		t.Fatalf("expected two command completions, got %d in %q", got, content)
	}
}

func TestShutdownGraceAllowsActiveRunToFinish(t *testing.T) {
	logPath := tempLogPath(t)

	s := newTestScheduler(t, helperCommand("sleep-write", logPath, "100ms"))
	s.ShutdownGrace = caddy.Duration(time.Second)
	requireStart(t, s)

	s.startRun(s.jobList[0])
	waitForContent(t, logPath, "START", 3*time.Second)

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	content := readFile(t, logPath)
	if !strings.Contains(content, "DONE") {
		t.Fatalf("expected command to finish within shutdown grace, got %q", content)
	}
}

func TestShutdownGraceCancelsActiveRun(t *testing.T) {
	logPath := tempLogPath(t)

	s := newTestScheduler(t, helperCommand("sleep-write", logPath, "2s"))
	s.ShutdownGrace = caddy.Duration(100 * time.Millisecond)
	requireStart(t, s)

	s.startRun(s.jobList[0])
	waitForContent(t, logPath, "START", 3*time.Second)

	started := time.Now()
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
	elapsed := time.Since(started)

	if elapsed > time.Second {
		t.Fatalf("expected shutdown to cancel before command finished, took %s", elapsed)
	}

	content := readFile(t, logPath)
	if strings.Contains(content, "DONE") {
		t.Fatalf("expected command to be cancelled before completion, got %q", content)
	}
}

func TestMultipleJobsRunIndependently(t *testing.T) {
	firstLog := tempLogPath(t)
	secondLog := tempLogPath(t)

	s := &Scheduler{
		Jobs: []*Job{
			{
				Name:    "first",
				Command: helperCommand("sleep-write", firstLog, "50ms"),
				Timeout: caddy.Duration(5 * time.Second),
				Overlap: OverlapSkip,
			},
			{
				Name:    "second",
				Command: helperCommand("sleep-write", secondLog, "50ms"),
				Timeout: caddy.Duration(5 * time.Second),
				Overlap: OverlapSkip,
			},
		},
		ShutdownGrace: caddy.Duration(time.Second),
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	requireStart(t, s)

	if len(s.jobList) != 2 {
		t.Fatalf("expected two prepared jobs, got %d", len(s.jobList))
	}
	s.startRun(s.jobList[0])
	s.startRun(s.jobList[1])

	waitForContent(t, firstLog, "START", 3*time.Second)
	waitForContent(t, secondLog, "START", 3*time.Second)
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	if got := strings.Count(readFile(t, firstLog), "DONE"); got != 1 {
		t.Fatalf("expected first job to finish once, got %d", got)
	}
	if got := strings.Count(readFile(t, secondLog), "DONE"); got != 1 {
		t.Fatalf("expected second job to finish once, got %d", got)
	}
}

func TestSchedulerHelperProcess(t *testing.T) {
	args := helperArgs()
	if len(args) == 0 {
		return
	}

	switch args[0] {
	case "sleep-write":
		if len(args) != 3 {
			t.Fatalf("usage: sleep-write <path> <duration>")
		}
		duration, err := time.ParseDuration(args[2])
		if err != nil {
			t.Fatalf("invalid duration: %v", err)
		}
		appendLine(t, args[1], "START")
		time.Sleep(duration)
		appendLine(t, args[1], "DONE")
	default:
		t.Fatalf("unknown helper command %q", args[0])
	}
}

func newTestScheduler(t *testing.T, command []string) *Scheduler {
	t.Helper()

	return &Scheduler{
		Job: Job{
			Command: command,
			Timeout: caddy.Duration(5 * time.Second),
		},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func requireStart(t *testing.T, s *Scheduler) {
	t.Helper()
	if err := s.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
}

func helperCommand(args ...string) []string {
	command := []string{os.Args[0], "-test.run=TestSchedulerHelperProcess", "--"}
	return append(command, args...)
}

func helperArgs() []string {
	for i, arg := range os.Args {
		if arg == "--" {
			return os.Args[i+1:]
		}
	}
	return nil
}

func tempLogPath(t *testing.T) string {
	t.Helper()

	file, err := os.CreateTemp("", "pogo_scheduler_test_*.log")
	if err != nil {
		t.Fatalf("create temp log: %v", err)
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		t.Fatalf("close temp log: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Remove(path)
	})

	return path
}

func appendLine(t *testing.T, path, line string) {
	t.Helper()

	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open helper log: %v", err)
	}
	defer func() { _ = file.Close() }()

	if _, err := file.WriteString(line + "\n"); err != nil {
		t.Fatalf("write helper log: %v", err)
	}
}

func waitForContent(t *testing.T, path, expected string, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(readFile(t, path), expected) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for %q in %s; got %q", expected, path, readFile(t, path))
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	return string(content)
}
