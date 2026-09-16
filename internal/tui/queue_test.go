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

// The rail is meant to read as a queue: what needs you first, whatever its age.
func TestGroupRowsPutsWaitingFirst(t *testing.T) {
	in := []dashRow{
		qrow("a-idle", claude.StateIdle, 5),
		qrow("b-working", claude.StateWorking, 10),
		qrow("c-waiting", claude.StateWaiting, 60),
		qrow("d-stuck", claude.StateStuck, 90),
		qrow("e-unknown", claude.StateUnknown, 1),
	}
	got := order(groupRows(in))
	want := []string{"c-waiting", "d-stuck", "b-working", "a-idle", "e-unknown"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

// Recency ordering has to survive inside a group, since that is what the store
// already sorted by and it is still the tiebreak that matters.
func TestGroupRowsKeepsOrderWithinAGroup(t *testing.T) {
	in := []dashRow{
		qrow("newer", claude.StateWaiting, 1),
		qrow("older", claude.StateWaiting, 90),
		qrow("working", claude.StateWorking, 2),
	}
	got := order(groupRows(in))
	if got[0] != "newer" || got[1] != "older" {
		t.Errorf("order = %v, want newer before older", got)
	}
}

func TestGroupRowsDoesNotMutateTheInput(t *testing.T) {
	in := []dashRow{qrow("idle", claude.StateIdle, 5), qrow("waiting", claude.StateWaiting, 60)}
	_ = groupRows(in)
	if in[0].Branch != "idle" {
		t.Errorf("input was reordered: %v", order(in))
	}
}

func TestSidebarShowsGroupHeaders(t *testing.T) {
	d := Dashboard{width: 120, height: 40}
	d.rows = groupRows([]dashRow{
		qrow("fix/rounding", claude.StateWaiting, 60),
		qrow("feature/login", claude.StateWorking, 2),
		qrow("chore/deps", claude.StateIdle, 120),
	})

	out := d.renderSidebar(d.rows, 40, 20)
	for _, want := range []string{"NEEDS YOU", "WORKING", "QUIET", "fix/rounding"} {
		if !strings.Contains(out, want) {
			t.Errorf("sidebar missing %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "NEEDS YOU") > strings.Index(out, "WORKING") {
		t.Error("NEEDS YOU is not the first group")
	}
}

// A non-claude agent, or a worktree with no transcript yet, has no live state.
// Heading that whole list "QUIET" tells the user nothing, so the plain rail
// stays in that case.
func TestSidebarSkipsHeadersWithNoLiveState(t *testing.T) {
	d := Dashboard{width: 120, height: 40}
	d.rows = []dashRow{qrow("fix/one", claude.StateUnknown, 5), qrow("fix/two", claude.StateUnknown, 9)}

	out := d.renderSidebar(d.rows, 40, 20)
	if strings.Contains(out, "QUIET") {
		t.Errorf("headers shown with no live state:\n%s", out)
	}
	if !strings.Contains(out, "THREADS") {
		t.Errorf("plain rail header missing:\n%s", out)
	}
}

// The cursor's own rendered line has to stay on screen. Windowing over rows
// rather than lines would let it slide off by one line per header.
func TestSidebarKeepsTheCursorVisibleWithHeaders(t *testing.T) {
	var rows []dashRow
	for i := 0; i < 8; i++ {
		rows = append(rows, qrow("wait/"+string(rune('a'+i)), claude.StateWaiting, i))
	}
	for i := 0; i < 8; i++ {
		rows = append(rows, qrow("work/"+string(rune('a'+i)), claude.StateWorking, i))
	}
	for i := 0; i < 8; i++ {
		rows = append(rows, qrow("idle/"+string(rune('a'+i)), claude.StateIdle, i))
	}

	d := Dashboard{width: 120, height: 40}
	d.rows = groupRows(rows)

	for _, cursor := range []int{0, 7, 8, 15, 16, 23} {
		d.cursor = cursor
		out := d.renderSidebar(d.rows, 40, 10)
		if !strings.Contains(out, d.rows[cursor].Branch) {
			t.Errorf("cursor %d (%s) scrolled out of view:\n%s", cursor, d.rows[cursor].Branch, out)
		}
	}
}

// Rows reorder on every tick now, so an index-based cursor points at a
// different thread after a state flip. The cursor has to track the thread.
func TestCursorFollowsTheThreadAcrossAReorder(t *testing.T) {
	before := groupRows([]dashRow{
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
	after := groupRows([]dashRow{
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
	d := Dashboard{width: 120, height: 40, rows: groupRows([]dashRow{
		qrow("a", claude.StateWaiting, 1),
		qrow("b", claude.StateWorking, 2),
	})}
	d.cursor = 1

	m, _ := d.Update(dashLoadedMsg{rows: groupRows([]dashRow{qrow("a", claude.StateWaiting, 1)})})
	d = m.(Dashboard)

	if d.cursor < 0 || d.cursor >= len(d.visibleRows()) {
		t.Errorf("cursor = %d, out of range for %d rows", d.cursor, len(d.visibleRows()))
	}
}
