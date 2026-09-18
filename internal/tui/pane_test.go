package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sandbye/norn/internal/config"
	"github.com/sandbye/norn/internal/pty"
)

// The pane's whole promise is that an agent receives what you typed. If a key
// encodes wrongly, the agent's own prompts and shortcuts break in ways that
// look like the agent misbehaving rather than like norn dropping a byte.
func TestEncodeKey(t *testing.T) {
	cases := []struct {
		name string
		msg  tea.KeyMsg
		want string
	}{
		{"letters", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hi")}, "hi"},
		{"enter is carriage return", tea.KeyMsg{Type: tea.KeyEnter}, "\r"},
		{"backspace is DEL", tea.KeyMsg{Type: tea.KeyBackspace}, "\x7f"},
		{"tab", tea.KeyMsg{Type: tea.KeyTab}, "\t"},
		{"escape", tea.KeyMsg{Type: tea.KeyEsc}, "\x1b"},
		{"up arrow", tea.KeyMsg{Type: tea.KeyUp}, "\x1b[A"},
		{"down arrow", tea.KeyMsg{Type: tea.KeyDown}, "\x1b[B"},
		{"page up", tea.KeyMsg{Type: tea.KeyPgUp}, "\x1b[5~"},
		{"delete", tea.KeyMsg{Type: tea.KeyDelete}, "\x1b[3~"},
		{"alt+letter", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b"), Alt: true}, "\x1bb"},
		{"ctrl+c interrupts", tea.KeyMsg{Type: tea.KeyCtrlC}, "\x03"},
		{"ctrl+a", tea.KeyMsg{Type: tea.KeyCtrlA}, "\x01"},
		{"ctrl+z", tea.KeyMsg{Type: tea.KeyCtrlZ}, "\x1a"},
		{"unicode", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("æ")}, "æ"},
	}
	for _, c := range cases {
		if got := string(encodeKey(c.msg)); got != c.want {
			t.Errorf("%s: encodeKey = %q, want %q", c.name, got, c.want)
		}
	}
}

// ctrl+c must reach the agent rather than quitting norn, because interrupting
// a run is the most common reason to be in the pane at all.
func TestCtrlCIsNotNornsKey(t *testing.T) {
	if got := string(encodeKey(tea.KeyMsg{Type: tea.KeyCtrlC})); got != "\x03" {
		t.Fatalf("ctrl+c encoded as %q, want the interrupt byte", got)
	}
	// Nothing is reserved outright any more: norn's keys sit behind a leader,
	// and the leader itself is reachable by pressing it twice.
	if cfg := (config.Config{}).PaneLeaderKey(); cfg == "ctrl+c" || cfg == "ctrl+b" {
		t.Fatalf("the default leader is %q, which the agent or tmux needs", cfg)
	}
}

// A pane whose attachment has ended must not swallow keys. This is the bug
// that made a failed `tmux attach` unescapable: the pty existed, so the pane
// counted as open, and every key including ctrl+c went into a dead terminal.
func TestDeadPaneDoesNotTrapYou(t *testing.T) {
	term, err := pty.Start(exec.Command("sh", "-c", "exit 1"), 40, 10)
	if err != nil {
		t.Fatal(err)
	}
	<-term.Done()

	d := Dashboard{pane: paneState{term: term, taskID: "t1", role: "logic"}}
	if !d.pane.open() {
		t.Fatal("the pane is not open, so the test proves nothing")
	}
	if !d.pane.dead() {
		t.Fatal("a pane whose program exited did not report dead")
	}

	m, _ := d.Update(key("x"))
	if got := m.(Dashboard); got.pane.open() {
		t.Fatal("an ordinary key did not escape a dead pane")
	}
}

// A live pane keeps every key except the one norn reserved.
func TestLivePaneKeepsItsKeys(t *testing.T) {
	term, err := pty.Start(exec.Command("sh", "-c", "sleep 5"), 40, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()

	d := Dashboard{pane: paneState{term: term, taskID: "t1", role: "logic"}}
	m, _ := d.Update(key("x"))
	if got := m.(Dashboard); !got.pane.open() {
		t.Fatal("a normal key closed a live pane instead of reaching the agent")
	}
	// Leader, then ←: one key alone must not leave, or an agent that uses the
	// arrow keys could never receive them.
	m, _ = d.Update(key("left"))
	d = m.(Dashboard)
	if !d.pane.open() {
		t.Fatal("a bare ← left the pane, so the agent can never receive one")
	}
	m, _ = d.Update(tea.KeyMsg{Type: tea.KeyCtrlA})
	d = m.(Dashboard)
	if !d.pane.armed {
		t.Fatal("the leader did not arm the pane")
	}
	m, _ = d.Update(key("left"))
	if got := m.(Dashboard); got.pane.open() {
		t.Fatal("leader then ← did not leave the pane")
	}
}

// A strand starts in auto mode. Manual is Claude Code's default, and with
// three strands running you are looking at one pane at most: the others would
// stop at the first prompt and wait for someone who is elsewhere.
func TestStrandStartsInAutoMode(t *testing.T) {
	args := strandCmd(config.Config{}, config.AgentConfig{Command: "claude"}, "/wt/logic", "").Args
	joined := " " + strings.Join(args, " ") + " "
	if !strings.Contains(joined, " --permission-mode auto ") {
		t.Fatalf("strand launch is not in auto mode: %v", args)
	}

	if !strings.Contains(joined, " --prompt-suggestions false ") {
		t.Fatalf("a strand can still have a suggestion typed into it by Tab: %v", args)
	}

	// The key you press yourself is unchanged: entering a worktree with `o` is
	// you sitting down at it, and that is when manual is the right default.
	if got := makeAgentCmd(config.Config{}, config.AgentConfig{Command: "claude"}, "/wt/logic", false, "").Args; strings.Contains(strings.Join(got, " "), "--permission-mode") {
		t.Fatalf("an interactive launch changed its permission mode: %v", got)
	}
}

// Effort is per model, so opus can think harder than sonnet without a flag on
// every launch. A model with no entry falls back to "default".
func TestEffortPerModel(t *testing.T) {
	cfg := config.Config{Effort: map[string]string{"opus": "xhigh", "default": "high"}}

	args := makeAgentCmd(cfg, config.AgentConfig{Command: "claude"}, "/wt/x", false, "opus").Args
	if !strings.Contains(" "+strings.Join(args, " ")+" ", " --effort xhigh ") {
		t.Fatalf("opus did not get its effort: %v", args)
	}
	args = makeAgentCmd(cfg, config.AgentConfig{Command: "claude"}, "/wt/x", false, "sonnet").Args
	if !strings.Contains(" "+strings.Join(args, " ")+" ", " --effort high ") {
		t.Fatalf("a model with no entry did not fall back to default: %v", args)
	}
	args = makeAgentCmd(config.Config{}, config.AgentConfig{Command: "claude"}, "/wt/x", false, "opus").Args
	if strings.Contains(strings.Join(args, " "), "--effort") {
		t.Fatalf("an unconfigured repo passed an effort anyway: %v", args)
	}
}

// The brief goes to a launched agent by path, not by value. Passed as an
// argument it made the tmux spawn fail with "command too long" for whichever
// role had the longest brief, and the planner's is the longest of all.
func TestStrandBriefGoesByPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".worktree.md"), []byte("a brief"), 0o644); err != nil {
		t.Fatal(err)
	}
	args := strandCmd(config.Config{}, config.AgentConfig{Command: "claude"}, dir, "").Args
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--append-system-prompt-file "+filepath.Join(dir, ".worktree.md")) {
		t.Fatalf("the brief is not passed by path: %v", args)
	}
	if strings.Contains(joined, "a brief") {
		t.Fatalf("the brief's contents are still on the command line: %v", args)
	}

	// A worktree with no brief passes neither, rather than an empty flag.
	bare := strandCmd(config.Config{}, config.AgentConfig{Command: "claude"}, t.TempDir(), "").Args
	if strings.Contains(strings.Join(bare, " "), "--append-system-prompt") {
		t.Fatalf("a missing brief still produced the flag: %v", bare)
	}
}
