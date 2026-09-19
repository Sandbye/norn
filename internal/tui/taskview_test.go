package tui

import (
	"strings"
	"testing"

	"github.com/sandbye/norn/internal/state"
)

func strandOf(task, role, branch string) dashRow {
	return dashRow{Session: state.Session{TaskID: task, Role: role, Branch: branch, Path: "/w/" + role},
		TaskTrunk: "f/" + task + "/trunk", WorktreeAlive: true}
}

// The left column is tasks, so rows have to group by task while keeping rail
// order, and the trunk leads each group because it is what the rest land on.
func TestTaskCardsGroupAndLeadWithTheTrunk(t *testing.T) {
	vis := []dashRow{
		strandOf("a", "logic", "f/a/logic"),
		{Session: state.Session{Branch: "chore/readme", Path: "/w/loose"}},
		strandOf("a", "integration", "f/a/trunk"),
		strandOf("b", "web", "f/b/web"),
	}
	vis[2].Branch = "f/a/trunk"

	cards, loose, looseCursor := taskCards(vis, 1)
	if len(cards) != 2 || cards[0].id != "a" || cards[1].id != "b" {
		t.Fatalf("cards = %+v, want one per task in rail order", cards)
	}
	if got := cards[0].rows[0].Role; got != "integration" {
		t.Errorf("first row of the card is %q, want the trunk", got)
	}
	if len(loose) != 1 || looseCursor != 0 {
		t.Errorf("a task-less worktree was not kept separate: loose=%d cursor=%d", len(loose), looseCursor)
	}
	if cards[0].hasCursor || cards[1].hasCursor {
		t.Error("a card claims the cursor that is on a loose worktree")
	}

	cards, _, _ = taskCards(vis, 0)
	if !cards[0].hasCursor {
		t.Error("the card holding the cursor's row does not know it")
	}
}

// The headline is the reason to look at a task, so it names the action, and
// needing a person beats anything a machine can do on its own.
func TestCardHeadlinePicksTheAction(t *testing.T) {
	ApplyTheme("nord")
	trunk := strandOf("a", "integration", "f/a/trunk")
	trunk.Branch = "f/a/trunk"

	logic := strandOf("a", "logic", "f/a/logic")
	if got := cardHeadline([]dashRow{trunk, logic}, 40); !strings.Contains(got, "can start") {
		t.Errorf("headline = %q", got)
	}

	logic.Ahead = 2
	if got := cardHeadline([]dashRow{trunk, logic}, 40); !strings.Contains(got, "to land") {
		t.Errorf("headline = %q", got)
	}

	logic.Bell = true
	if got := cardHeadline([]dashRow{trunk, logic}, 40); !strings.Contains(got, "need you") {
		t.Errorf("a task with a question does not say so: %q", got)
	}

	logic.Bell, logic.Ahead, logic.Run = false, 0, state.RunMerged
	if got := cardHeadline([]dashRow{trunk, logic}, 40); !strings.Contains(got, "ready for review") {
		t.Errorf("a finished task says %q", got)
	}
}

// Old worktrees pile up, and a row you cannot see is a row you cannot act on:
// the cursor's own row survives the trim.
func TestTrimLooseKeepsTheCursorVisible(t *testing.T) {
	loose := []dashRow{
		{Session: state.Session{Branch: "one", Path: "/1"}},
		{Session: state.Session{Branch: "two", Path: "/2"}},
		{Session: state.Session{Branch: "three", Path: "/3"}},
		{Session: state.Session{Branch: "four", Path: "/4"}},
	}
	shown, extra := trimLoose(loose, 3, 2)
	if extra != 2 {
		t.Errorf("extra = %d, want 2", extra)
	}
	var paths []string
	for _, r := range shown {
		paths = append(paths, r.Path)
	}
	if strings.Join(paths, ",") != "/1,/4" {
		t.Errorf("shown = %v, want the newest plus the cursor's own row", paths)
	}

	if shown, extra := trimLoose(loose[:2], -1, 5); extra != 0 || len(shown) != 2 {
		t.Errorf("a short list was trimmed: %d shown, %d extra", len(shown), extra)
	}
}
