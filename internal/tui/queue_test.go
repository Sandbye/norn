package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/sandbye/norn/internal/claude"
	"github.com/sandbye/norn/internal/state"
)

func qrow(branch string, s claude.AgentState, ageMin int) dashRow {
	return dashRow{
		Session: state.Session{
			Branch: branch, Path: "/wt/" + branch, Repo: "app",
			Status: state.StatusActive, LastActivityAt: time.Now().Add(-time.Duration(ageMin) * time.Minute),
		},
		WorktreeAlive: true,
		AgentState:    s,
	}
}

func order(rows []dashRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Branch)
	}
	return out
}

// A list you are pointing at has to hold still: urgency belongs in what a row
// says, not in where it sits, or the row under the cursor changes while you
// read it.
func TestOrderRowsDoesNotMoveRowsWhenStateChanges(t *testing.T) {
	in := []dashRow{
		qrow("a-idle", claude.StateIdle, 5),
		qrow("b-working", claude.StateWorking, 10),
		qrow("c-waiting", claude.StateWaiting, 60),
	}
	before := order(orderRows(in))

	// The same rows, one of them now asking a question.
	in[0].AgentState = claude.StateWaiting
	if after := order(orderRows(in)); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Errorf("a state change reordered the list: %v then %v", before, after)
	}
}

// Tasks are what you came to work on; a worktree belonging to none is one you
// left behind, so it sits below them however recent it is.
func TestOrderRowsPutsTasksAboveLooseWorktrees(t *testing.T) {
	loose := qrow("chore/readme", claude.StateIdle, 1)
	strand := qrow("f/t/logic", claude.StateIdle, 900)
	strand.TaskID, strand.TaskTrunk = "t", "f/t/trunk"

	got := order(orderRows([]dashRow{loose, strand}))
	if got[0] != "f/t/logic" {
		t.Errorf("order = %v, want the task's strand first", got)
	}
}

// Inside a task the trunk leads, because it is what the others land on and
// where the pull request comes from.
func TestOrderRowsLeadsATaskWithItsTrunk(t *testing.T) {
	logic := qrow("f/t/logic", claude.StateIdle, 1)
	logic.TaskID, logic.TaskTrunk = "t", "f/t/trunk"
	trunk := qrow("f/t/trunk", claude.StateIdle, 90)
	trunk.TaskID, trunk.TaskTrunk = "t", "f/t/trunk"

	got := order(orderRows([]dashRow{logic, trunk}))
	if got[0] != "f/t/trunk" {
		t.Errorf("order = %v, want the trunk first", got)
	}
}

func TestOrderRowsDoesNotMutateTheInput(t *testing.T) {
	in := []dashRow{qrow("idle", claude.StateIdle, 5), qrow("waiting", claude.StateWaiting, 60)}
	_ = orderRows(in)
	if in[0].Branch != "idle" {
		t.Errorf("input was reordered: %v", order(in))
	}
}

// Every loose worktree is one line with its age, and the section says how many
// there are, since the column shows only the recent few.
func TestLooseSectionCountsWhatItDoesNotShow(t *testing.T) {
	ApplyTheme("nord")
	var rows []dashRow
	for i := 0; i < 9; i++ {
		rows = append(rows, qrow("chore/"+string(rune('a'+i)), claude.StateIdle, i))
	}
	d := Dashboard{width: 120, height: 40, rows: orderRows(rows)}

	cards, loose, looseCursor := taskCards(d.visibleRows(), 0)
	out := stripANSI(d.renderTaskList(cards, loose, looseCursor, 40, 20))
	if !strings.Contains(out, "LOOSE · 9") {
		t.Errorf("the section does not say how many there are:\n%s", out)
	}
	if !strings.Contains(out, "older") {
		t.Errorf("the rows it left out are not accounted for:\n%s", out)
	}
}

// The cursor's own line has to stay on screen: the left column trims what it
// cannot fit, and trimming away the row you are pointing at is the one cut it
// must not make.
func TestLeftColumnKeepsTheCursorVisible(t *testing.T) {
	ApplyTheme("nord")
	var rows []dashRow
	for i := 0; i < 24; i++ {
		rows = append(rows, qrow("wait/"+string(rune('a'+i)), claude.StateWaiting, i))
	}

	d := Dashboard{width: 120, height: 40}
	d.rows = orderRows(rows)

	for _, cursor := range []int{0, 7, 15, 23} {
		d.cursor = cursor
		cards, loose, looseCursor := taskCards(d.visibleRows(), d.cursor)
		out := stripANSI(d.renderTaskList(cards, loose, looseCursor, 40, 10))
		if !strings.Contains(out, d.rows[cursor].Branch) {
			t.Errorf("cursor %d (%s) was trimmed out of the column:\n%s", cursor, d.rows[cursor].Branch, out)
		}
	}
}

// Rows reorder on every tick now, so an index-based cursor points at a
// different thread after a state flip. The cursor has to track the thread.
func TestCursorFollowsTheThreadAcrossAReorder(t *testing.T) {
	before := orderRows([]dashRow{
		qrow("fix/rounding", claude.StateWaiting, 60),
		qrow("feature/login", claude.StateWorking, 2),
		qrow("chore/deps", claude.StateIdle, 120),
	})
	d := Dashboard{width: 120, height: 40, rows: before}

	// Point at the working thread, which sits below the waiting one.
	for i, r := range d.rows {
		if r.Branch == "feature/login" {
			d.cursor = i
		}
	}
	if d.rows[d.cursor].Branch != "feature/login" {
		t.Fatalf("setup: cursor on %s", d.rows[d.cursor].Branch)
	}

	// It finishes its turn. loadCmd sorts by activity before grouping, so the
	// thread that just moved arrives first, which is what shifts every index
	// below it.
	after := orderRows([]dashRow{
		qrow("feature/login", claude.StateWaiting, 0),
		qrow("fix/rounding", claude.StateWaiting, 61),
		qrow("chore/deps", claude.StateIdle, 121),
	})
	if after[0].Branch != "feature/login" || after[1].Branch != "fix/rounding" {
		t.Fatalf("setup: expected the reorder to move feature/login to the top, got %v", order(after))
	}
	m, _ := d.Update(dashLoadedMsg{rows: after})
	d = m.(Dashboard)

	if got := d.visibleRows()[d.cursor].Branch; got != "feature/login" {
		t.Errorf("cursor moved to %q, want it still on feature/login", got)
	}
}

// A thread that disappears (deleted worktree) must not leave the cursor out of
// range or silently pointing at a neighbour without clamping.
func TestCursorSurvivesAThreadDisappearing(t *testing.T) {
	d := Dashboard{width: 120, height: 40, rows: orderRows([]dashRow{
		qrow("a", claude.StateWaiting, 1),
		qrow("b", claude.StateWorking, 2),
	})}
	d.cursor = 1

	m, _ := d.Update(dashLoadedMsg{rows: orderRows([]dashRow{qrow("a", claude.StateWaiting, 1)})})
	d = m.(Dashboard)

	if d.cursor < 0 || d.cursor >= len(d.visibleRows()) {
		t.Errorf("cursor = %d, out of range for %d rows", d.cursor, len(d.visibleRows()))
	}
}

// Reading the open question in the pane is what replaces a trip into the
// session, so it has to actually render.
func TestDetailShowsTheOpenQuestion(t *testing.T) {
	d := Dashboard{width: 120, height: 40}
	r := qrow("fix/rounding", claude.StateWaiting, 1)
	r.Question = "Should I drop the column or keep it nullable?"

	out := d.renderDetail(r, 60)
	if !strings.Contains(out, "asked") {
		t.Errorf("no asked block:\n%s", out)
	}
	if !strings.Contains(out, "keep it nullable?") {
		t.Errorf("question text missing:\n%s", out)
	}

	quiet := qrow("feature/login", claude.StateWorking, 1)
	if out := d.renderDetail(quiet, 60); strings.Contains(out, "asked") {
		t.Errorf("asked block shown for a thread with no question:\n%s", out)
	}
}

// A long message must not push the rest of the pane off screen, and the tail is
// the part worth keeping: that is where the question is.
func TestQuestionLinesKeepsTheTailAndCaps(t *testing.T) {
	text := strings.Repeat("Some preamble that goes on. ", 40) + "So: option A or option B?"

	got := questionLines(text, 40, questionPaneLines)
	if len(got) > questionPaneLines {
		t.Errorf("%d lines, want at most %d", len(got), questionPaneLines)
	}
	if !strings.Contains(strings.Join(got, " "), "option A or option B?") {
		t.Errorf("tail dropped:\n%s", strings.Join(got, "\n"))
	}

	// Trailing blank lines must not eat the window.
	padded := questionLines("one line\n\n\n\n\n\n\n\n", 40, questionPaneLines)
	if len(padded) != 1 || strings.TrimSpace(padded[0]) != "one line" {
		t.Errorf("padding was not trimmed: %q", padded)
	}
}
