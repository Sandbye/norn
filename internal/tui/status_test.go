package tui

import (
	"strings"
	"testing"

	"github.com/sandbye/norn/internal/claude"
	"github.com/sandbye/norn/internal/state"
)

func statusRow(s state.Session) dashRow {
	return dashRow{Session: s, TaskTrunk: "f/t/trunk", WorktreeAlive: true}
}

// Every strand has exactly one status, and the one shown is the one that
// decides what the person does next: acting on it beats watching it.
func TestStrandStatusPicksTheActionableState(t *testing.T) {
	ApplyTheme("nord")
	for _, c := range []struct {
		name string
		row  dashRow
		want string
	}{
		{"plain worktree has no strand status",
			dashRow{Session: state.Session{Branch: "chore/x"}}, ""},
		{"failed beats everything",
			func() dashRow {
				r := statusRow(state.Session{Role: "logic", Run: state.RunFailed})
				r.Ahead, r.Bell = 3, true
				return r
			}(), "failed"},
		{"a question beats work in progress",
			func() dashRow {
				r := statusRow(state.Session{Role: "logic", Run: state.RunRunning})
				r.Bell, r.AgentState = true, claude.StateWorking
				return r
			}(), "needs you"},
		{"committed work waiting to land",
			func() dashRow {
				r := statusRow(state.Session{Role: "logic", Run: state.RunRunning})
				r.Ahead = 2
				return r
			}(),
			"2 commit(s)"},
		{"work that exists but cannot land",
			func() dashRow {
				r := statusRow(state.Session{Role: "logic", Run: state.RunRunning})
				r.Uncommitted = true
				return r
			}(),
			"uncommitted"},
		{"sequenced behind its pair",
			func() dashRow { r := statusRow(state.Session{Role: "logic"}); r.WaitsFor = "logic-tests"; return r }(),
			"after its tests"},
		{"sequenced behind another role",
			func() dashRow { r := statusRow(state.Session{Role: "docs"}); r.WaitsFor = "cli"; return r }(),
			"after cli"},
		{"the reviewer waits for all of them",
			func() dashRow { r := statusRow(state.Session{Role: "review"}); r.WaitsFor = "*"; return r }(),
			"after the code"},
		{"nothing in the way",
			statusRow(state.Session{Role: "logic"}), "can start"},
		{"landed",
			statusRow(state.Session{Role: "logic", Run: state.RunMerged}), "landed"},
	} {
		got, _ := strandStatus(c.row)
		if got != c.want {
			t.Errorf("%s: status = %q, want %q", c.name, got, c.want)
		}
		if len(got) > statusWidth {
			t.Errorf("%s: status %q is wider than the column", c.name, got)
		}
	}
}

// The mark is the peripheral-vision version of the word, so the two must never
// disagree about whether the strand wants a person.
func TestGlyphAgreesWithStatus(t *testing.T) {
	ApplyTheme("nord")
	needsYou := func() dashRow { r := statusRow(state.Session{Role: "logic"}); r.Uncommitted = true; return r }()
	if !strings.Contains(statusGlyph(needsYou), "◆") {
		t.Error("a strand holding uncommitted work does not carry the mark that means act on me")
	}
	landed := statusRow(state.Session{Role: "logic", Run: state.RunMerged})
	if !strings.Contains(statusGlyph(landed), "✓") {
		t.Error("a landed strand is not marked as done")
	}
}

// The task header answers "what do I do with this task", so it names an action
// while one exists and falls back to a ratio only when none does.
func TestTaskHeaderNamesTheNextAction(t *testing.T) {
	ApplyTheme("nord")
	trunk := dashRow{Session: state.Session{Role: "integration", Branch: "f/t/trunk", TaskID: "t"}, TaskTrunk: "f/t/trunk"}
	logic := dashRow{Session: state.Session{Role: "logic", Branch: "f/t/logic", TaskID: "t"}, TaskTrunk: "f/t/trunk"}

	if got := taskProgress([]dashRow{trunk, logic}, "t"); !strings.Contains(got, "can start") {
		t.Errorf("a task with a startable strand says %q", got)
	}

	logic.Ahead = 3
	if got := taskProgress([]dashRow{trunk, logic}, "t"); !strings.Contains(got, "1 strand(s) to land") {
		t.Errorf("a task with landable work says %q", got)
	}

	logic.Ahead, logic.Bell = 0, true
	if got := taskProgress([]dashRow{trunk, logic}, "t"); !strings.Contains(got, "need you") {
		t.Errorf("a task with a question says %q", got)
	}

	logic.Bell, logic.Run = false, state.RunMerged
	if got := taskProgress([]dashRow{trunk, logic}, "t"); !strings.Contains(got, "ready for review") {
		t.Errorf("a finished task with no PR says %q", got)
	}

	trunk.PRNumber = 7
	if got := taskProgress([]dashRow{trunk, logic}, "t"); !strings.Contains(got, "1/1 landed") {
		t.Errorf("a task whose PR is open says %q", got)
	}
}
