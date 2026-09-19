package tui

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/charmbracelet/lipgloss"
	"github.com/sandbye/norn/internal/plan"
	"github.com/sandbye/norn/internal/state"
)

// A plan is longer than a terminal, so the view must never render taller than
// the terminal it is drawn into.
func TestPlanViewFitsTerminal(t *testing.T) {
	long := strings.Repeat("this strand owns the workspace resolution path and its tests. ", 12)
	p := plan.Plan{Summary: "split by service", Strands: []plan.Strand{
		{Role: "core", Brief: long}, {Role: "web", After: "core", Expect: "green", Brief: long},
		{Role: "docs", Brief: long},
	}}
	for _, h := range []int{18, 24, 30, 60} {
		for _, expand := range []bool{false, true} {
			d := Dashboard{width: 120, height: h, showPlan: true, planExpand: expand,
				planRow: dashRow{Session: state.Session{Role: "plan"}, Plan: &p}}
			if got := lipgloss.Height(d.renderPlanView()); got > h {
				t.Fatalf("height %d expand=%v: view rendered %d lines", h, expand, got)
			}
		}
	}
}

// A plan is a proposal by one strand, so it must not appear on the strands it
// proposes, and it must stop appearing once they exist.
func TestPlanProposalOnlyPlannerUntilCarriedOut(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	p := plan.Plan{Strands: []plan.Strand{{Role: "core", Brief: "b"}, {Role: "web", Brief: "b"}}}
	writePlan(t, "t1", p)

	store := &state.Store{Sessions: []state.Session{
		{TaskID: "t1", Role: "plan"}, {TaskID: "t1", Role: "core"},
	}}

	if got := planProposal(store, "plan", store.Sessions[0]); got == nil {
		t.Fatal("planner lost its proposal while web is missing")
	}
	if got := planProposal(store, "plan", store.Sessions[1]); got != nil {
		t.Fatal("core was offered the plan that creates core")
	}
	store.Sessions = append(store.Sessions, state.Session{TaskID: "t1", Role: "web"})
	if got := planProposal(store, "plan", store.Sessions[0]); got != nil {
		t.Fatal("plan still proposed after every strand exists")
	}
}

func writePlan(t *testing.T, taskID string, p plan.Plan) {
	t.Helper()
	if err := os.MkdirAll(plan.Dir(taskID), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := yaml.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plan.Path(taskID), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// Coming back from a review, the cursor lands on the task norn was told to
// return to, not on whatever row a reload left it on.
func TestFocusTaskPlacesTheCursor(t *testing.T) {
	rows := []dashRow{
		{Session: state.Session{TaskID: "t2", Role: "logic", Branch: "f/t2/logic", Path: "/w/logic", Status: state.StatusActive}},
		{Session: state.Session{TaskID: "t1", Role: "contract", Branch: "f/t1/contract", Path: "/w/contract", Status: state.StatusActive}},
	}
	d := Dashboard{width: 120, height: 40, cursor: 0, focusTask: "t1"}
	m, _ := d.Update(dashLoadedMsg{rows: rows})
	got := m.(Dashboard)

	vis := got.visibleRows()
	if got.cursor >= len(vis) || vis[got.cursor].TaskID != "t1" {
		t.Fatalf("cursor is on %d, which is not a row of the task norn returned to", got.cursor)
	}
	if got.focusTask != "" {
		t.Error("the request to focus a task survived the load, so it would fight the cursor forever")
	}
}
