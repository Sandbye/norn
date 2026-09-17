package tui

import (
	"testing"

	"github.com/sandbye/norn/internal/claude"
	"github.com/sandbye/norn/internal/state"
)

func strandRow(role, branch, goal string) dashRow {
	r := qrow(branch, claude.StateIdle, 1)
	r.TaskID, r.Role, r.TaskGoal, r.Run = "t1", role, goal, state.RunRunning
	return r
}

// The picker matches whatever you happen to remember about a strand: its role,
// its branch, or the task it belongs to. Typing one of the three is how you
// jump without going back to the rail to look.
func TestSwitcherMatchesAnythingYouRemember(t *testing.T) {
	rows := []dashRow{
		strandRow("logic", "feature/880-watch-rebuild/logic", "monorepo rebuild in watch mode"),
		strandRow("tests", "feature/880-watch-rebuild/tests", "monorepo rebuild in watch mode"),
		qrow("chore/deps", claude.StateIdle, 1), // not a strand
	}

	if got := switcherMatches(rows, ""); len(got) != 2 {
		t.Fatalf("empty query = %d rows, want both strands and no plain worktree", len(got))
	}
	for _, q := range []string{"logic", "880", "watch mode", "LOGIC"} {
		got := switcherMatches(rows, q)
		if len(got) == 0 {
			t.Fatalf("query %q matched nothing", q)
		}
		if q != "880" && q != "watch mode" && got[0].Role != "logic" {
			t.Fatalf("query %q matched %q first", q, got[0].Role)
		}
	}
	if got := switcherMatches(rows, "nothing like this"); len(got) != 0 {
		t.Fatalf("a query matching nothing returned %d rows", len(got))
	}
}

// Enter acts on the row you can see highlighted, including when the cursor sat
// past the end of a list that just shrank under a longer query.
func TestSwitcherSelectionFollowsTheList(t *testing.T) {
	s := switcherState{active: true, cursor: 5}
	s.matched = switcherMatches([]dashRow{
		strandRow("logic", "feature/x/logic", "a task"),
		strandRow("tests", "feature/x/tests", "a task"),
	}, "")

	row, ok := s.selected()
	if !ok || row.Role != "tests" {
		t.Fatalf("selected = %+v, %v; want the last row when the cursor is past the end", row, ok)
	}
	if _, ok := (switcherState{}).selected(); ok {
		t.Fatal("an empty picker reported a selection")
	}
}
