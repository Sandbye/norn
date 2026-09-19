package tui

import (
	"fmt"
	"sync"
	"time"
)

// What a landing is doing while it appears to do nothing.
//
// `L` runs the repo's verify before it merges, and a real suite takes minutes.
// A single "landing x…" line for all of that reads as a freeze, which is how a
// working gate gets mistaken for a hang and killed.

// verifyStep is the command a landing is on, and since when.
type verifyStep struct {
	role    string
	cmd     string
	index   int // 1-based
	total   int
	started time.Time
}

var (
	verifyMu   sync.Mutex
	verifySeen = map[string]verifyStep{} // role → current step
)

// setVerifyStep records what a strand's gate is running now.
func setVerifyStep(role, cmd string, index, total int) {
	verifyMu.Lock()
	verifySeen[role] = verifyStep{role: role, cmd: cmd, index: index, total: total, started: time.Now()}
	verifyMu.Unlock()
}

// clearVerifyStep forgets a finished landing, so a stale command never shows
// under the next one.
func clearVerifyStep(role string) {
	verifyMu.Lock()
	delete(verifySeen, role)
	verifyMu.Unlock()
}

// verifyLine is what the rail shows for a landing in flight, or "" when that
// strand is not running a gate.
func verifyLine(role string) string {
	verifyMu.Lock()
	step, ok := verifySeen[role]
	verifyMu.Unlock()
	if !ok {
		return ""
	}
	return fmt.Sprintf("verifying %s (%d/%d): %s · %s",
		step.role, step.index, step.total, step.cmd, roundSeconds(time.Since(step.started)))
}

// roundSeconds is the elapsed time, at the resolution a person reads: seconds
// while it is quick, minutes once it stops being.
func roundSeconds(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

// landingInFlight reports whether any gate is running, which is what decides
// the rail ticks fast enough to show the seconds moving.
func landingInFlight() bool {
	verifyMu.Lock()
	defer verifyMu.Unlock()
	return len(verifySeen) > 0
}
