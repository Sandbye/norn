package tui

import (
	"strings"
	"testing"

	"github.com/sandbye/norn/internal/state"
)

// A strand that has not started is either sequenced behind another or ready to
// go, and the board has to say which: "nothing outstanding" on both reads as a
// task that has quietly stopped.
func TestBoardSaysWhatAStrandIsWaitingFor(t *testing.T) {
	ApplyTheme("nord")

	waiting := dashRow{Session: state.Session{Role: "logic", Branch: "f/t/logic", TaskID: "t"},
		TaskTrunk: "f/t/trunk", WaitsFor: "tests"}
	if got := boardState(waiting); !strings.Contains(got, "waits for tests") {
		t.Errorf("a sequenced strand reads as %q", got)
	}

	ready := dashRow{Session: state.Session{Role: "logic", Branch: "f/t/logic", TaskID: "t"},
		TaskTrunk: "f/t/trunk"}
	if got := boardState(ready); !strings.Contains(got, "ready to start") {
		t.Errorf("a startable strand reads as %q", got)
	}

	if got := boardSummary([]dashRow{ready}); !strings.Contains(got, "can start now") {
		t.Errorf("summary does not offer the next action: %q", got)
	}
	if got := boardSummary([]dashRow{waiting}); strings.Contains(got, "can start now") {
		t.Errorf("summary offers R for a strand that cannot start: %q", got)
	}
}

// Work written but not committed is invisible to L, so a strand holding it
// must not read the same as one that wrote nothing at all.
func TestBoardSeparatesUncommittedWorkFromNoWork(t *testing.T) {
	ApplyTheme("nord")
	base := state.Session{Role: "tests", Branch: "f/t/tests", TaskID: "t", Run: state.RunRunning}

	idle := dashRow{Session: base, TaskTrunk: "f/t/trunk"}
	dirty := dashRow{Session: base, TaskTrunk: "f/t/trunk", Uncommitted: true}
	if strings.Contains(boardState(idle), "not committed") {
		t.Error("a clean strand reads as holding uncommitted work")
	}
	if got := boardState(dirty); !strings.Contains(got, "not committed") {
		t.Errorf("uncommitted work is invisible on the board: %q", got)
	}
	if got := boardSummary([]dashRow{dirty}); !strings.Contains(got, "not committed") {
		t.Errorf("summary hides the reason nothing can land: %q", got)
	}
}
