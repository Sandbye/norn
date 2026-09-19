package tui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/sandbye/norn/internal/claude"
	"github.com/sandbye/norn/internal/state"
)

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// stripANSI drops the styling so a test can assert on layout (indentation).
func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// trow is qrow plus the task fields, for a worktree that plays a role in a task.
func trow(branch, taskID, role string, s claude.AgentState, ageMin int) dashRow {
	r := qrow(branch, s, ageMin)
	r.TaskID, r.Role = taskID, role
	r.TaskGoal, r.TaskTrunk = "split a task", "feature/multi/CU-1"
	return r
}

// A task's worktrees are one piece of work, so they stay adjacent whatever
// each one is doing, and the task keeps its place in the column.
func TestOrderRowsKeepsATaskTogether(t *testing.T) {
	in := []dashRow{
		qrow("chore/deps", claude.StateWorking, 5),
		trow("feature/multi/CU-1", "t1", "trunk", claude.StateIdle, 30),
		qrow("fix/other", claude.StateIdle, 2),
		trow("feature/multi/CU-1/logic", "t1", "logic", claude.StateWaiting, 60),
	}
	got := order(orderRows(in))
	// The loose pair follow the task, most recently touched first.
	want := []string{"feature/multi/CU-1", "feature/multi/CU-1/logic", "fix/other", "chore/deps"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

// The left column shows a task once, and the right column names its strands by
// role: the shared part of the branch is already in the task's own line.
func TestTaskColumnsRenderATaskOnce(t *testing.T) {
	ApplyTheme("nord")
	d := Dashboard{width: 160, height: 40}
	d.rows = orderRows([]dashRow{
		trow("feature/multi/CU-1", "t1", "trunk", claude.StateWaiting, 30),
		trow("feature/multi/CU-1/logic", "t1", "logic", claude.StateWorking, 5),
		qrow("chore/deps", claude.StateWorking, 2),
	})

	out := stripANSI(d.View())
	if n := strings.Count(out, "split a task"); n != 2 {
		t.Fatalf("the task reads %d times, want once on the left and once as the right column's title:\n%s", n, out)
	}
	for _, want := range []string{"trunk", "logic", "chore/deps", "LOOSE"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the view is missing %q:\n%s", want, out)
		}
	}
}

// A task with no goal yet still needs a name, so it falls back to the trunk
// branch rather than rendering an unlabeled line.
func TestTaskLabelFallsBackToTrunk(t *testing.T) {
	r := trow("feature/multi/CU-1", "t1", "trunk", claude.StateWaiting, 30)
	r.TaskGoal = ""
	if got := taskLabel(r); !strings.Contains(got, "feature/multi/CU-1") {
		t.Fatalf("label = %q, want the trunk branch", got)
	}
}

// A headless strand has no transcript to read a live state from, so its run
// state is what it says about itself. A failed one asks for a person.
func TestStatusFromRunState(t *testing.T) {
	ApplyTheme("nord")
	failed := trow("feature/multi/CU-1/assets", "t1", "assets", claude.StateIdle, 5)
	failed.Run = state.RunFailed
	if got, _ := strandStatus(failed); got != "failed" {
		t.Fatalf("failed strand reads %q", got)
	}

	running := trow("feature/multi/CU-1/logic", "t1", "logic", claude.StateIdle, 5)
	running.Run = state.RunRunning
	if got, _ := strandStatus(running); got != "working" {
		t.Fatalf("running strand reads %q", got)
	}

	blocked := trow("feature/multi/CU-1/trunk", "t1", "integration", claude.StateIdle, 5)
	blocked.TaskBlocked = "logic: merge conflict"
	if label := taskLabel(blocked); !strings.Contains(label, "blocked") {
		t.Fatalf("task label = %q, want it to say blocked", label)
	}
}
