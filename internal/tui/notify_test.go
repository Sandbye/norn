package tui

import (
	"testing"

	"github.com/sandbye/norn/internal/claude"
	"github.com/sandbye/norn/internal/state"
)

func row(path string, s claude.AgentState) dashRow {
	return dashRow{Session: state.Session{Path: path, Branch: path}, AgentState: s}
}

func branches(rows []dashRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Branch)
	}
	return out
}

// The ping is worth firing once, on the flip. Firing every tick while a thread
// sits waiting is what makes people turn the feature off.
func TestAgentTransitionsFiresOncePerFlip(t *testing.T) {
	seen := seedAgentStates([]dashRow{row("a", claude.StateWorking)})

	if got := agentTransitions(seen, []dashRow{row("a", claude.StateWorking)}); len(got) != 0 {
		t.Errorf("still working: got %v, want no notification", branches(got))
	}
	if got := branches(agentTransitions(seen, []dashRow{row("a", claude.StateWaiting)})); len(got) != 1 || got[0] != "a" {
		t.Fatalf("flip to waiting: got %v, want [a]", got)
	}
	if got := agentTransitions(seen, []dashRow{row("a", claude.StateWaiting)}); len(got) != 0 {
		t.Errorf("state held: got %v, want no second notification", branches(got))
	}
	if got := agentTransitions(seen, []dashRow{row("a", claude.StateWorking)}); len(got) != 0 {
		t.Errorf("back to working: got %v, want no notification", branches(got))
	}
	if got := branches(agentTransitions(seen, []dashRow{row("a", claude.StateWaiting)})); len(got) != 1 {
		t.Errorf("second flip: got %v, want one notification", got)
	}
}

// A thread already waiting when the dashboard opens is a condition, not an
// event: notifying for every one of them on startup is noise.
func TestFirstSightNeverNotifies(t *testing.T) {
	rows := []dashRow{row("a", claude.StateWaiting), row("b", claude.StateWorking)}

	if got := agentTransitions(nil, rows); got != nil {
		t.Errorf("first load: got %v, want nothing", branches(got))
	}

	seen := seedAgentStates(rows)
	if got := agentTransitions(seen, rows); len(got) != 0 {
		t.Errorf("after seeding: got %v, want nothing", branches(got))
	}

	// A worktree adopted later is first-sighted too, even mid-session.
	withNew := append(rows, row("c", claude.StateWaiting))
	if got := agentTransitions(seen, withNew); len(got) != 0 {
		t.Errorf("newly adopted row: got %v, want nothing", branches(got))
	}
	// ...but its next flip does notify.
	if got := agentTransitions(seen, []dashRow{row("c", claude.StateWorking)}); len(got) != 0 {
		t.Fatalf("c to working: got %v", branches(got))
	}
	if got := branches(agentTransitions(seen, []dashRow{row("c", claude.StateWaiting)})); len(got) != 1 {
		t.Errorf("c flip after being known: got %v, want one", got)
	}
}

func TestStuckAlsoNeedsTheUser(t *testing.T) {
	seen := seedAgentStates([]dashRow{row("a", claude.StateWorking)})
	if got := branches(agentTransitions(seen, []dashRow{row("a", claude.StateStuck)})); len(got) != 1 {
		t.Errorf("flip to stuck: got %v, want one notification", got)
	}
	// waiting -> stuck is not a fresh call for attention, the user is already called.
	if got := agentTransitions(seen, []dashRow{row("a", claude.StateWaiting)}); len(got) != 0 {
		t.Errorf("stuck to waiting: got %v, want none", branches(got))
	}
}

// idle is a timeout, not an answer: a thread going quiet must not ping.
func TestIdleAndUnknownNeverNotify(t *testing.T) {
	seen := seedAgentStates([]dashRow{row("a", claude.StateWorking)})
	for _, s := range []claude.AgentState{claude.StateIdle, claude.StateUnknown} {
		if got := agentTransitions(seen, []dashRow{row("a", s)}); len(got) != 0 {
			t.Errorf("flip to %q: got %v, want none", s, branches(got))
		}
		seen["a"] = claude.StateWorking
	}
}
