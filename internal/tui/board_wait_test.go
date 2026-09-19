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
