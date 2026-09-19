package tui

import (
	"strings"
	"testing"
	"time"
)

// The real path: runVerify drives the store, and a slow command is visible
// while it runs rather than only after it ends.
func TestRunVerifyReportsWhileItRuns(t *testing.T) {
	t.Cleanup(func() { clearVerifyStep("slow") })
	seen := make(chan string, 4)

	go func() {
		passed, failing, _ := runVerify(t.TempDir(),
			[]string{"true", "sleep 2", "false"},
			func(cmd string, i, n int) { setVerifyStep("slow", cmd, i, n) })
		seen <- failing
		if passed {
			t.Error("a verify list ending in false reported passed")
		}
	}()

	deadline := time.After(5 * time.Second)
	for {
		select {
		case failing := <-seen:
			if failing != "false" {
				t.Fatalf("failing command = %q, want false", failing)
			}
			return
		case <-deadline:
			t.Fatal("verify never finished")
		case <-time.After(300 * time.Millisecond):
			if line := verifyLine("slow"); strings.Contains(line, "sleep 2") {
				if !strings.Contains(line, "2/3") {
					t.Errorf("gate line lost its position: %q", line)
				}
				// Seen mid-run, which is the whole point.
				continue
			}
		}
	}
}
