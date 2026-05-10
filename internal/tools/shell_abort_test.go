//go:build !windows

package tools

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestShellAbort_ProcessGroupKilled verifies that cancelling ctx after spawning a
// command that forks background children kills the entire process group within 4s
// and leaves no orphan sleep processes.
//
// The command `sh -c "sleep <N> & sleep <N> & wait"` spawns two background sleeps
// using a per-invocation-unique duration N. The orphan check filters ps output by
// that exact duration — without this, stale `sleep 60` processes left over by an
// earlier test run (or any concurrent shell on the dev machine) cause false
// failures. The duration is picked from the nanosecond clock and clamped to a
// "long enough not to exit during the test" range.
func TestShellAbort_ProcessGroupKilled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group kill not supported on Windows")
	}

	// Per-test-unique sleep duration so findOrphanSleeps does not pick up
	// processes spawned by other tests or by leftover state on the host.
	// Range [60, 1659] keeps the value plausible to `sleep` (POSIX accepts
	// any non-negative integer, but small ranges avoid rare locale/quirks).
	sleepArg := 60 + (time.Now().UnixNano() % 1600)
	command := fmt.Sprintf("sleep %d & sleep %d & wait", sleepArg, sleepArg)

	tool := NewExecTool(t.TempDir(), false)
	tool.timeout = 10 * time.Second // generous outer timeout

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan *Result, 1)
	go func() {
		done <- tool.executeOnHost(ctx, command, t.TempDir())
	}()

	// Give the shell time to fork the sleep processes.
	time.Sleep(100 * time.Millisecond)

	// Cancel ctx — should trigger SIGTERM → 3s grace → SIGKILL.
	cancel()

	// Verify tool returns within 4s (3s grace + 1s buffer).
	select {
	case result := <-done:
		if result == nil {
			t.Fatal("expected non-nil result after abort")
		}
		// Result should indicate abortion, not normal completion.
		if !result.IsError {
			t.Errorf("expected IsError=true after abort, got ForLLM=%q", result.ForLLM)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("executeOnHost did not return within 4s after ctx cancel")
	}

	// Give the OS a moment to reap the killed processes.
	time.Sleep(200 * time.Millisecond)

	// Verify no orphan sleep processes remain. We filter on the exact unique
	// duration we spawned with so the check cannot collide with unrelated
	// `sleep` processes on the host.
	needle := fmt.Sprintf("sleep %d", sleepArg)
	orphans := findOrphanSleeps(t, needle)
	if len(orphans) > 0 {
		t.Errorf("found %d orphan %q process(es) after abort: %v", len(orphans), needle, orphans)
	}
}

// findOrphanSleeps returns PIDs of any remaining processes whose ps-line
// matches the given needle. Uses `ps aux` output parsed in Go — avoids
// pgrep availability issues on macOS.
func findOrphanSleeps(t *testing.T, needle string) []string {
	t.Helper()

	out, err := exec.Command("ps", "aux").Output()
	if err != nil {
		t.Logf("ps aux failed (non-fatal): %v", err)
		return nil
	}

	var found []string
	for line := range strings.SplitSeq(string(out), "\n") {
		// Skip the ps command itself.
		if !strings.Contains(line, needle) || strings.Contains(line, "ps aux") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			found = append(found, fields[1]) // PID is column 2 in ps aux
		}
	}
	return found
}
