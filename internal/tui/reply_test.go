package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sandbye/norn/internal/claude"
)

// --continue resumes a finished session, not a running one, so offering reply
// on a working thread would send an answer into a new session instead.
func TestCanReplyOnlyWhenWaiting(t *testing.T) {
	// A transcript has to exist for the path, because --continue happily starts
	// a new session when it does not, so canReply checks rather than assumes.
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	const wt = "/wt/fix/rounding"
	slug := strings.NewReplacer("/", "-", ".", "-").Replace(wt)
	dir := filepath.Join(home, "projects", slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		path  string
		state claude.AgentState
		alive bool
		want  bool
	}{
		{"waiting with a session", wt, claude.StateWaiting, true, true},
		{"stuck", wt, claude.StateStuck, true, false},
		{"working", wt, claude.StateWorking, true, false},
		{"idle", wt, claude.StateIdle, true, false},
		{"unknown", wt, claude.StateUnknown, true, false},
		{"worktree gone", wt, claude.StateWaiting, false, false},
		{"no transcript to continue", "/wt/never/ran", claude.StateWaiting, true, false},
	}
	for _, c := range cases {
		r := qrow("fix/rounding", c.state, 1)
		r.Path, r.WorktreeAlive = c.path, c.alive
		if got := canReply(r); got != c.want {
			t.Errorf("%s: canReply = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestReplyInputEditing(t *testing.T) {
	r := replyState{active: true}

	for _, s := range strings.Split("yes b", "") {
		if !r.handleKey(s) {
			t.Fatalf("key %q not consumed", s)
		}
	}
	if r.text != "yes b" {
		t.Fatalf("text = %q", r.text)
	}
	r.handleKey("backspace")
	if r.text != "yes " {
		t.Errorf("after backspace: %q", r.text)
	}
	r.handleKey("ctrl+u")
	if r.text != "" {
		t.Errorf("after ctrl+u: %q", r.text)
	}
	// Enter and esc belong to the caller, which needs the model to send.
	if r.handleKey("enter") || r.handleKey("esc") {
		t.Error("handleKey consumed enter/esc")
	}
	// A closed input consumes nothing, or it would swallow navigation.
	closed := replyState{}
	if closed.handleKey("j") {
		t.Error("closed input consumed a key")
	}
}

// While the input is open, letters have to type rather than trigger actions:
// "d" is drop and "o" opens the agent.
func TestReplyInputSwallowsActionKeys(t *testing.T) {
	d := Dashboard{width: 120, height: 40, rows: groupRows([]dashRow{qrow("fix/rounding", claude.StateWaiting, 1)})}
	d.reply = replyState{active: true, path: "/wt/fix/rounding", branch: "fix/rounding"}

	for _, s := range []string{"d", "o", "/", "j"} {
		m, _ := d.Update(key(s))
		d = m.(Dashboard)
	}
	if d.reply.text != "do/j" {
		t.Errorf("text = %q, want the keys typed as text", d.reply.text)
	}
	if d.filter.active {
		t.Error("/ opened the filter while replying")
	}
	if d.quit {
		t.Error("an action key fired while replying")
	}
}

func TestReplyEscapeCancelsWithoutSending(t *testing.T) {
	d := Dashboard{width: 120, height: 40, rows: groupRows([]dashRow{qrow("fix/rounding", claude.StateWaiting, 1)})}
	d.reply = replyState{active: true, text: "yes", path: "/wt/x", branch: "fix/rounding"}

	m, cmd := d.Update(key("esc"))
	d = m.(Dashboard)
	if d.reply.active || d.reply.text != "" {
		t.Errorf("esc left the input open: %+v", d.reply)
	}
	if cmd != nil {
		t.Error("esc produced a command")
	}
}

// An empty reply must not be sent: --continue with an empty prompt would still
// resume the session and spend a turn on nothing.
func TestEmptyReplyIsNotSent(t *testing.T) {
	d := Dashboard{width: 120, height: 40, rows: groupRows([]dashRow{qrow("fix/rounding", claude.StateWaiting, 1)})}
	d.reply = replyState{active: true, text: "   ", path: "/wt/x", branch: "fix/rounding"}

	m, cmd := d.Update(key("enter"))
	d = m.(Dashboard)
	if cmd != nil {
		t.Error("an empty reply produced a send command")
	}
	if d.reply.active {
		t.Error("input stayed open")
	}
}

func TestReplyOutcomeIsReported(t *testing.T) {
	d := Dashboard{width: 120, height: 40}

	m, _ := d.Update(replySentMsg{branch: "fix/rounding"})
	if got := m.(Dashboard).reply.sent; !strings.Contains(got, "answered fix/rounding") {
		t.Errorf("success not reported: %q", got)
	}

	m, _ = d.Update(replySentMsg{branch: "fix/rounding", err: errReply("boom")})
	if got := m.(Dashboard).reply.sent; !strings.Contains(got, "failed") || !strings.Contains(got, "boom") {
		t.Errorf("failure not reported: %q", got)
	}
}
