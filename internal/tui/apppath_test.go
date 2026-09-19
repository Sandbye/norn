package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sandbye/norn/internal/claude"
	"github.com/sandbye/norn/internal/config"
)

// The exact path a user takes: press i, type, press enter. Enter is also the
// "cd into this worktree and quit" key, so if the input never opened, the
// keystrokes act instead of typing and enter drops you out of norn.
func TestAppReplyPathDoesNotQuit(t *testing.T) {
	stubClaude(t)
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	wt := t.TempDir()
	slug := strings.NewReplacer("/", "-", ".", "-").Replace(wt)
	if err := os.MkdirAll(filepath.Join(home, "projects", slug), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "projects", slug, "abc.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := qrow("fix/rounding", claude.StateWaiting, 1)
	r.Path = wt
	a := App{current: ViewThreads, mainDir: "/repo"}
	a.dashboard.rows = orderRows([]dashRow{r})
	a.dashboard.cfg = config.Config{Agent: config.AgentConfig{Command: "claude"}}

	m, _ := a.Update(key("i"))
	a = m.(App)
	if !a.dashboard.reply.active {
		t.Fatalf("i did not open the input, status was %q", a.dashboard.reply.sent)
	}

	for _, s := range []string{"y", "e", "s"} {
		m, _ = a.Update(key(s))
		a = m.(App)
	}
	if a.dashboard.reply.text != "yes" {
		t.Fatalf("typed text = %q", a.dashboard.reply.text)
	}

	m, _ = a.Update(key("enter"))
	a = m.(App)
	if a.quit {
		t.Error("enter quit the app instead of sending the reply")
	}
}
