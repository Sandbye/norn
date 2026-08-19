package tui

import (
	"errors"
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
