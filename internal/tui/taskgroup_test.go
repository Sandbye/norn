package tui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/sandbye/norn/internal/claude"
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

// A task's worktrees are one piece of work, so they stay adjacent and travel to
// the bucket of whichever role needs you. Splitting them would put the rest of
// the task three headers below the role that asked a question.
func TestGroupRowsClustersATask(t *testing.T) {
	in := []dashRow{
		qrow("chore/deps", claude.StateWorking, 5),
		trow("feature/multi/CU-1", "t1", "trunk", claude.StateIdle, 30),
		qrow("fix/other", claude.StateIdle, 2),
		trow("feature/multi/CU-1/logic", "t1", "logic", claude.StateWaiting, 60),
	}
	got := order(groupRows(in))
	want := []string{"feature/multi/CU-1", "feature/multi/CU-1/logic", "chore/deps", "fix/other"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

// Standalone worktrees keep the old queue order exactly: the cluster rule only
// applies to rows that carry a task id.
func TestGroupRowsLeavesStandaloneRowsAlone(t *testing.T) {
	in := []dashRow{
		qrow("a-idle", claude.StateIdle, 5),
		qrow("b-working", claude.StateWorking, 10),
		qrow("c-waiting", claude.StateWaiting, 60),
	}
	got := order(groupRows(in))
	want := []string{"c-waiting", "b-working", "a-idle"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

// The rail shows the task once, as a header over its roles, and names each row
// by the role it plays. The full branch of every role is noise under a header
// that already carries the shared part.
func TestSidebarRendersATaskAsOneGroup(t *testing.T) {
	d := Dashboard{width: 120, height: 40}
	d.rows = groupRows([]dashRow{
		trow("feature/multi/CU-1", "t1", "trunk", claude.StateWaiting, 30),
		trow("feature/multi/CU-1/logic", "t1", "logic", claude.StateWorking, 5),
		qrow("chore/deps", claude.StateWorking, 2),
	})

	out := d.renderSidebar(d.rows, 40, 20)
	if n := strings.Count(out, "split a task"); n != 1 {
		t.Fatalf("task header rendered %d times, want 1:\n%s", n, out)
	}
	for _, want := range []string{"NEEDS YOU", "trunk", "logic", "chore/deps"} {
		if !strings.Contains(out, want) {
			t.Fatalf("sidebar missing %q:\n%s", want, out)
		}
	}
	// Role rows indent under the header; the standalone row does not.
	for _, line := range strings.Split(out, "\n") {
		plain := stripANSI(line)
		switch {
		case strings.Contains(plain, "trunk"), strings.Contains(plain, "logic"):
			if !strings.HasPrefix(plain, "  ") {
				t.Errorf("role row is not indented: %q", plain)
			}
		case strings.Contains(plain, "chore/deps"):
			if strings.HasPrefix(plain, " ") {
				t.Errorf("standalone row gained an indent: %q", plain)
			}
		}
	}
}

// A task with no goal yet still needs a header, so it falls back to the trunk
// branch rather than rendering an unlabeled indent.
func TestSidebarTaskHeaderFallsBackToTrunk(t *testing.T) {
	d := Dashboard{width: 120, height: 40}
	r := trow("feature/multi/CU-1", "t1", "trunk", claude.StateWaiting, 30)
	r.TaskGoal = ""
	d.rows = groupRows([]dashRow{r})

	out := stripANSI(d.renderSidebar(d.rows, 40, 20))
	if !strings.Contains(out, "feature/multi/CU-1") {
		t.Fatalf("header did not fall back to the trunk branch:\n%s", out)
	}
}
