package tui

import (
	"errors"
	"github.com/sandbye/norn/internal/task"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// The confirm must be a one-shot. It used to stay true, so every message that
// reached the New tab mid-create fired a second `git worktree add` on the same
// branch — the duplicate failed and flashed an error while the first create
// succeeded and launched the agent.
func TestCreateConfirmFiresOnce(t *testing.T) {
	a := App{current: ViewCreate, width: 120, height: 40}
	a.create = newCreateModel([]string{"main"})
	a.create.hint = "some task"
	a.create.confirmed = true

	next, cmd := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	app := next.(App)
	if cmd == nil {
		t.Fatal("first message with confirmed=true returned no create command")
	}
	if app.create.confirmed {
		t.Error("confirmed still set after firing: a later message would create again")
	}
	if !app.create.creating {
		t.Error("creating not set, so the panel has nothing to render")
	}

	// Anything arriving while the create is in flight must not fire another.
	after, cmd2 := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd2 != nil {
		t.Errorf("second message fired another command: %T", cmd2)
	}
	if after.(App).create.confirmed {
		t.Error("confirmed re-set while a create was in flight")
	}
}

// The in-flight panel must say something: huh renders an empty view once the
// form completes, which read as norn glitching out.
func TestCreatingViewNotBlank(t *testing.T) {
	m := newCreateModel([]string{"main"})
	m.width, m.height = 120, 40
	m.hint = "fix the thing"
	m = m.startCreating()

	out := m.View()
	if !strings.Contains(out, "Creating worktree") {
		t.Errorf("in-flight view lacks a progress line:\n%q", out)
	}
	if !strings.Contains(out, "fix the thing") {
		t.Errorf("in-flight view drops the hint:\n%q", out)
	}
}

// A failed create returns to the form with the hint intact, under the error
// banner, so it can be retried without retyping.
func TestCreateFailureReturnsToForm(t *testing.T) {
	a := App{current: ViewCreate, width: 120, height: 40}
	a.create = newCreateModel([]string{"main", "develop"})
	a.create.hint = "fix the thing"
	a.create = a.create.startCreating()

	next, _ := a.Update(errMsg{errors.New("branch already exists")})
	app := next.(App)
	if app.err == nil {
		t.Fatal("error not recorded")
	}
	if app.create.creating {
		t.Error("still marked creating after a failure")
	}
	if !app.create.focused {
		t.Error("form not refocused, so the error banner sits under a blank panel")
	}
	if app.create.hint != "fix the thing" {
		t.Errorf("hint = %q, want it preserved for the retry", app.create.hint)
	}
	if !strings.Contains(app.View(), "branch already exists") {
		t.Error("error banner not rendered")
	}
}

// A keystroke clears a stale banner, but not one that just explained why the
// create in flight failed.
func TestCreateErrorSurvivesNextKey(t *testing.T) {
	a := App{current: ViewCreate, width: 120, height: 40}
	a.create = newCreateModel([]string{"main"})
	a.create = a.create.startCreating()
	a.err = errors.New("worktree add failed")

	next, _ := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if next.(App).err == nil {
		t.Error("banner cleared by the next keystroke while the create was in flight")
	}
}

// A task-seeded create must still offer the split. The roles picker is the only
// field left when the repo has one base branch, so a create started from the
// Tasks tab would otherwise confirm immediately and no task from the tracker
// could ever be split across roles.
func TestTaskSeededCreateOffersRoles(t *testing.T) {
	m := newCreateModel([]string{"main"})
	m.roles = []string{"integration", "logic"}
	m.form = m.buildForm()

	seeded := m.withTask(task.Task{ID: "71", Title: "headless roles"})
	if seeded.confirmed {
		t.Fatal("task-seeded create confirmed itself, so the roles picker never showed")
	}
	if seeded.baseBranch != "main" {
		t.Fatalf("base = %q, want the only base branch", seeded.baseBranch)
	}

	// Without roles there is nothing left to ask, so the old shortcut stands.
	plain := newCreateModel([]string{"main"})
	plain.form = plain.buildForm()
	if got := plain.withTask(task.Task{ID: "1", Title: "x"}); !got.confirmed {
		t.Fatal("a create with no choices left should confirm immediately")
	}
}

// A task is named by its number in conversation, so the picker has to find it
// that way: the filter searched title and group only, and typing "1067" or
// "#1067" matched nothing at all.
func TestFilterTasksByID(t *testing.T) {
	tasks := []task.Task{
		{ID: "1067", Title: "workspace include globs match directories"},
		{ID: "1075", Title: "unbundle cjs with css and same name files"},
		{ID: "959", Title: "external CSS assets not inlined"},
	}

	for _, q := range []string{"1067", "#1067", " 1067 "} {
		got := filterTasks(tasks, q)
		if len(got) == 0 || got[0].ID != "1067" {
			t.Fatalf("query %q gave %v, want 1067 first", q, ids(got))
		}
	}

	// A prefix still ranks the right one first rather than whatever the fuzzy
	// scorer liked in the titles.
	if got := filterTasks(tasks, "107"); len(got) == 0 || got[0].ID != "1075" {
		t.Fatalf("prefix query gave %v, want 1075 first", ids(got))
	}

	// Searching by words still works.
	if got := filterTasks(tasks, "css"); len(got) == 0 {
		t.Fatal("a word query stopped matching")
	}
}

func ids(ts []task.Task) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.ID
	}
	return out
}
