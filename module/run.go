package scheduler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

const maxLoggedBody = 4 << 10 // 4 KiB

func (s *Scheduler) runCommand(ctx context.Context) {
	started := time.Now()
	s.logger.Info("scheduler command started",
		slog.Any("command", s.Command),
		slog.String("dir", s.Dir),
	)

	cmd := exec.CommandContext(ctx, s.Command[0], s.Command[1:]...)
	if s.Dir != "" {
		cmd.Dir = s.Dir
	}

	out, err := cmd.CombinedOutput()
	duration := time.Since(started)

	if err != nil {
		attrs := []any{
			slog.String("output", string(out)),
			slog.Any("error", err),
			slog.Duration("duration", duration),
		}
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			s.logger.Error("scheduler command timed out", attrs...)
		case errors.Is(ctx.Err(), context.Canceled):
			s.logger.Warn("scheduler command cancelled", attrs...)
		default:
			s.logger.Error("scheduler command failed", attrs...)
		}
		return
	}

	if len(out) > 0 {
		s.logger.Info("scheduler command output", slog.String("output", string(out)))
	}
	s.logger.Info("scheduler command finished", slog.Duration("duration", duration))
}

func (s *Scheduler) runHTTP(ctx context.Context) {
	started := time.Now()
	s.logger.Info("scheduler http request started",
		slog.String("method", s.Method),
		slog.String("url", s.URL),
	)

	status, body, err := s.doHTTP(ctx)
	duration := time.Since(started)

	if err != nil {
		attrs := []any{
			slog.Any("error", err),
			slog.Duration("duration", duration),
		}
		if body != "" {
			attrs = append(attrs, slog.String("response", truncate(body, maxLoggedBody)))
		}
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			s.logger.Error("scheduler http request timed out", attrs...)
		case errors.Is(ctx.Err(), context.Canceled):
			s.logger.Warn("scheduler http request cancelled", attrs...)
		default:
			s.logger.Error("scheduler http request failed", attrs...)
		}
		return
	}

	s.logger.Info("scheduler http request finished",
		slog.Int("status", status),
		slog.Duration("duration", duration),
	)
}

// doHTTP performs a single HTTP(S) request and reports the response status.
// It returns an error only for transport-level failures, non-2xx responses, or
// an explicit expect_status mismatch.
func (s *Scheduler) doHTTP(ctx context.Context) (int, string, error) {
	var bodyReader io.Reader
	if s.Body != "" {
		bodyReader = strings.NewReader(s.Body)
	}

	req, err := http.NewRequestWithContext(ctx, s.Method, s.URL, bodyReader)
	if err != nil {
		return 0, "", fmt.Errorf("create request: %w", err)
	}
	for name, values := range s.Headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}

	client := s.httpClient
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxLoggedBody+1))
	if err != nil {
		return resp.StatusCode, "", fmt.Errorf("read response: %w", err)
	}
	body := string(respBody)
	if len(respBody) > maxLoggedBody {
		body = truncate(body, maxLoggedBody)
	}

	if s.ExpectStatus != 0 {
		if resp.StatusCode != s.ExpectStatus {
			return resp.StatusCode, body, fmt.Errorf(
				"unexpected status %d, expected %d", resp.StatusCode, s.ExpectStatus)
		}
		return resp.StatusCode, body, nil
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, body, fmt.Errorf("request failed with status %d", resp.StatusCode)
	}
	return resp.StatusCode, body, nil
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "...(truncated)"
}
