package tests

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSchedulerEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping long-running integration test in short mode")
	}

	// 1. Setup paths and artifacts
	_, currentFile, _, _ := runtime.Caller(0)
	testDir := filepath.Dir(currentFile)
	rootDir := filepath.Dir(testDir)

	// Locate the binary
	binPath := filepath.Join(rootDir, "caddy")
	if _, err := os.Stat(binPath); os.IsNotExist(err) {
		binPath = filepath.Join(rootDir, "frankenphp")
		if _, err := os.Stat(binPath); os.IsNotExist(err) {
			t.Fatalf("Binary not found. Build it first (e.g., 'xcaddy build'). Checked: %s", rootDir)
		}
	}

	// Create a temp file for the trigger
	triggerFile, err := os.CreateTemp("", "scheduler_trigger_*.txt")
	if err != nil {
		t.Fatalf("Failed to create temp trigger file: %v", err)
	}
	triggerPath := triggerFile.Name()
	_ = triggerFile.Close()
	// Ensure we clean up the file
	defer func() { _ = os.Remove(triggerPath) }()

	// 2. Define the command with PROPER QUOTING for Caddyfile
	var fullCommand string
	// Escape backslashes for Windows paths in Caddyfile string
	escapedTriggerPath := strings.ReplaceAll(triggerPath, "\\", "\\\\")

	if runtime.GOOS == "windows" {
		// cmd /C "echo TICK >> path"
		// The quotes around the echo command ensure Caddy parses it as one argument
		fullCommand = fmt.Sprintf("cmd /C \"echo TICK >> %s\"", escapedTriggerPath)
	} else {
		// sh -c "echo 'TICK' >> path"
		// The quotes around the script ensure 'sh' receives the whole string as the -c argument
		fullCommand = fmt.Sprintf("sh -c \"echo 'TICK' >> %s\"", escapedTriggerPath)
	}

	// 3. Construct Caddyfile
	caddyfileContent := fmt.Sprintf(`
	{
		admin off
		
		pogo_scheduler {
			command %s
			timeout 5s
		}
	}
	`, fullCommand)

	tmpCaddyfile, err := os.CreateTemp("", "Caddyfile_Scheduler_Test.*")
	if err != nil {
		t.Fatalf("Failed to create temp Caddyfile: %v", err)
	}
	defer func() { _ = os.Remove(tmpCaddyfile.Name()) }()

	if _, err := tmpCaddyfile.WriteString(caddyfileContent); err != nil {
		t.Fatalf("Failed to write Caddyfile: %v", err)
	}
	_ = tmpCaddyfile.Close()

	// 4. Start Caddy
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serverCmd := exec.CommandContext(ctx, binPath, "run", "--config", tmpCaddyfile.Name())
	serverCmd.Stdout = os.Stdout
	serverCmd.Stderr = os.Stderr

	if err := serverCmd.Start(); err != nil {
		t.Fatalf("Failed to start Caddy: %v", err)
	}
	t.Logf("Caddy started using config: %s", tmpCaddyfile.Name())

	// 5. Calculate Wait Time
	now := time.Now()
	nextMinute := now.Truncate(time.Minute).Add(time.Minute)
	// Add 2 seconds buffer for execution time
	waitDuration := time.Until(nextMinute) + 2*time.Second

	t.Logf("Current: %s", now.Format(time.TimeOnly))
	t.Logf("Next Tick: %s", nextMinute.Format(time.TimeOnly))
	t.Logf("Sleeping: %s", waitDuration.Round(time.Second))

	select {
	case <-time.After(waitDuration):
		// Time passed
	case <-ctx.Done():
		t.Fatal("Context cancelled unexpectedly")
	}

	// 6. Verification
	// We might need a small retry loop if disk I/O is slow (unlikely for echo)
	var output string
	for i := 0; i < 3; i++ {
		content, err := os.ReadFile(triggerPath)
		if err == nil {
			output = strings.TrimSpace(string(content))
			if strings.Contains(output, "TICK") {
				break
			}
		}
		time.Sleep(1 * time.Second)
	}

	t.Logf("Trigger File Content: %q", output)

	if !strings.Contains(output, "TICK") {
		t.Fatalf("Scheduler failed. Expected 'TICK' in file %s, got '%s'.\nDebug info: Check if sh/cmd is available in PATH.", triggerPath, output)
	}

	// 7. Cleanup
	cancel()
	_ = serverCmd.Wait()
}
