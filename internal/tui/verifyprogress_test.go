package tui

import (
	"strings"
	"testing"
	"time"
)

// A gate that takes minutes must say what it is running, or it reads as a
// hang and gets killed.
func TestVerifyLineNamesTheRunningCommand(t *testing.T) {
	t.Cleanup(func() { clearVerifyStep("logic") })

	if got := verifyLine("logic"); got != "" {
		t.Fatalf("a strand with no gate running reported %q", got)
	}
	if landingInFlight() {
		t.Fatal("nothing is landing, but a landing was reported in flight")
	}

	setVerifyStep("logic", "pnpm test", 2, 3)
	got := verifyLine("logic")
	for _, want := range []string{"logic", "2/3", "pnpm test"} {
		if !strings.Contains(got, want) {
			t.Errorf("gate line %q does not carry %q", got, want)
		}
	}
	if !landingInFlight() {
		t.Error("a running gate is not reported in flight")
	}

	clearVerifyStep("logic")
	if got := verifyLine("logic"); got != "" {
		t.Errorf("a finished gate still shows %q", got)
	}
}

// Elapsed time is read by a person deciding whether to wait, so it changes
// unit rather than growing to three digits of seconds.
func TestRoundSeconds(t *testing.T) {
	for _, c := range []struct {
		in   time.Duration
		want string
	}{
		{3 * time.Second, "3s"},
		{59 * time.Second, "59s"},
		{time.Minute + 5*time.Second, "1m05s"},
		{12*time.Minute + 30*time.Second, "12m30s"},
	} {
		if got := roundSeconds(c.in); got != c.want {
			t.Errorf("roundSeconds(%s) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The step store is written by the gate's goroutine and read by the render
// loop, so it has to survive that without the race detector complaining.
func TestVerifyStepIsSafeAcrossGoroutines(t *testing.T) {
	done := make(chan struct{})
	go func() {
		for i := 0; i < 200; i++ {
			setVerifyStep("logic", "go test ./...", 1, 2)
		}
		close(done)
	}()
	for i := 0; i < 200; i++ {
		_ = verifyLine("logic")
		_ = landingInFlight()
	}
	<-done
	clearVerifyStep("logic")
}
