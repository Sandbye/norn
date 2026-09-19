package tui

import (
	"testing"
	"time"

	"github.com/sandbye/norn/internal/state"
)

func navRow(task, role, path string, created time.Time) dashRow {
	r := dashRow{Session: state.Session{TaskID: task, Role: role, Path: path,
		Branch: "f/" + task + "/" + role, Status: state.StatusActive}}
	if task != "" {
		r.TaskTrunk, r.TaskCreatedAt, r.TaskGoal = "f/"+task+"/trunk", created, "task "+task
	}
	return r
}

func navRows() []dashRow {
	t0 := time.Now().Add(-2 * time.Hour)
	t1 := time.Now().Add(-time.Hour)
	return []dashRow{
		navRow("a", "integration", "/a/trunk", t0),
		navRow("a", "logic", "/a/logic", t0),
		navRow("a", "tests", "/a/tests", t0),
		navRow("b", "integration", "/b/trunk", t1),
		navRow("b", "web", "/b/web", t1),
		{Session: state.Session{Branch: "chore/readme", Path: "/loose", Status: state.StatusActive}},
	}
}

// j and k move between the things the left column draws. Walking strands with
// them is what made the left column sit still for three presses and then jump.
func TestJKWalkTasksNotStrands(t *testing.T) {
	rows := navRows()
	d := Dashboard{width: 160, height: 40, rows: rows, cursor: 0}

	d = d.moveLeftColumn(rows, 1)
	if got := rows[d.cursor].TaskID; got != "b" {
		t.Fatalf("one press down landed in task %q, want the next task", got)
	}

	d = d.moveLeftColumn(rows, 1)
	if got := rows[d.cursor].Path; got != "/loose" {
		t.Fatalf("past the last task the cursor is on %q, want the loose worktree", got)
	}

	d = d.moveLeftColumn(rows, 1)
	if got := rows[d.cursor].Path; got != "/loose" {
		t.Errorf("the column ran past its end to %q", got)
	}

	d = d.moveLeftColumn(rows, -2)
	if got := rows[d.cursor].TaskID; got != "a" {
		t.Errorf("two presses up landed in %q, want back at the first task", got)
	}
}

// J and K move inside the task the cursor is in, and stop at its ends: leaving
// a task is the left column's job, not something that happens by holding J.
func TestShiftJKWalkStrandsAndStopAtTheTaskBoundary(t *testing.T) {
	rows := navRows()
	d := Dashboard{width: 160, height: 40, rows: rows, cursor: 0}

	d = d.moveStrand(rows, 1)
	if got := rows[d.cursor].Role; got != "logic" {
		t.Fatalf("J moved to %q, want the next strand of the same task", got)
	}

	d = d.moveStrand(rows, 1)
	d = d.moveStrand(rows, 1)
	if got := rows[d.cursor]; got.TaskID != "a" || got.Role != "tests" {
		t.Errorf("J past the last strand left the task: %s/%s", got.TaskID, got.Role)
	}

	d = d.moveStrand(rows, -5)
	if got := rows[d.cursor].Role; got != "integration" {
		t.Errorf("K past the first strand landed on %q", got)
	}
}

// Coming back to a task returns you to the strand you were reading, not to the
// top of it.
func TestATaskRemembersItsStrand(t *testing.T) {
	rows := navRows()
	d := Dashboard{width: 160, height: 40, rows: rows, cursor: 0}

	d = d.moveStrand(rows, 2) // task a, third strand
	if rows[d.cursor].Role != "tests" {
		t.Fatalf("setup landed on %q", rows[d.cursor].Role)
	}

	d = d.moveLeftColumn(rows, 1) // into task b
	d = d.moveLeftColumn(rows, -1)
	if got := rows[d.cursor]; got.TaskID != "a" || got.Role != "tests" {
		t.Errorf("returning to the task landed on %s/%s, want the strand we left", got.TaskID, got.Role)
	}
}

// A worktree that belongs to no task has no strands, so the strand keys fall
// back to moving the column rather than doing nothing.
func TestStrandKeysOnALooseWorktreeMoveTheColumn(t *testing.T) {
	rows := navRows()
	d := Dashboard{width: 160, height: 40, rows: rows, cursor: 5}

	d = d.moveStrand(rows, -1)
	if got := rows[d.cursor].TaskID; got != "b" {
		t.Errorf("K on a loose worktree landed in %q, want the previous entry", got)
	}
}
