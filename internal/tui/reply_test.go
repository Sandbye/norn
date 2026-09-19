package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sandbye/norn/internal/claude"
	"github.com/sandbye/norn/internal/config"
	"github.com/sandbye/norn/internal/state"
)

// stubClaude puts an executable named claude on PATH. The dashboard gates the
// reply input on claude.Available(), which is a real PATH lookup, so without
// this the tests pass only on a machine that happens to have Claude Code
// installed and fail on every CI runner.
func stubClaude(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh" + "\n" + "exit 0" + "\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// seedWaitingThread returns a row that can genuinely be answered: a live
// worktree with a transcript, in waiting state.
func seedWaitingThread(t *testing.T) dashRow {
	t.Helper()
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
	return r
}

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
	d := Dashboard{width: 120, height: 40, rows: orderRows([]dashRow{qrow("fix/rounding", claude.StateWaiting, 1)})}
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
	d := Dashboard{width: 120, height: 40, rows: orderRows([]dashRow{qrow("fix/rounding", claude.StateWaiting, 1)})}
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
	d := Dashboard{width: 120, height: 40, rows: orderRows([]dashRow{qrow("fix/rounding", claude.StateWaiting, 1)})}
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

// A reply restarts real work and can run for minutes. Without the sent text and
// a moving spinner, the dashboard reads as hung, which is how it read the first
// time it was used for real.
func TestReplyShowsWhatWasSentWhileRunning(t *testing.T) {
	row := seedWaitingThread(t)
	d := Dashboard{width: 120, height: 40, rows: orderRows([]dashRow{row})}
	d.reply = replyState{active: true, text: "option B please", path: row.Path, branch: row.Branch, target: row}

	m, cmd := d.Update(key("enter"))
	d = m.(Dashboard)
	if cmd == nil {
		t.Fatal("enter produced no command")
	}
	if d.reply.pending != "option B please" {
		t.Errorf("pending = %q, want the sent text", d.reply.pending)
	}
	if out := d.View(); !strings.Contains(out, "option B please") {
		t.Errorf("sent text is not on screen:\n%s", out)
	}

	m, _ = d.Update(replySentMsg{branch: "fix/rounding"})
	d = m.(Dashboard)
	if d.reply.pending != "" {
		t.Errorf("pending not cleared: %q", d.reply.pending)
	}
	if !strings.Contains(d.reply.sent, "option B please") {
		t.Errorf("outcome drops what was sent: %q", d.reply.sent)
	}
}

// A pasted paragraph must not reflow the dashboard.
func TestQuoteOneLine(t *testing.T) {
	if got := quoteOneLine("yes\n  do   that"); got != `"yes do that"` {
		t.Errorf("got %s", got)
	}
	long := quoteOneLine(strings.Repeat("word ", 40))
	if n := len([]rune(long)); n > 62 {
		t.Errorf("%d runes, want it cut", n)
	}
	if !strings.HasSuffix(long, `…"`) {
		t.Errorf("cut is not marked: %s", long[len(long)-10:])
	}
}

// App sees keys before Dashboard does, so an input it doesn't know about leaks
// every app-level key: m jumped to the main checkout mid-word, and 1-5 switched
// tabs. capturing() is the existing guard; a new input has to register with it.
func TestAppKeysDoNotLeakWhileReplying(t *testing.T) {
	a := App{current: ViewThreads, mainDir: "/repo"}
	a.dashboard.rows = orderRows([]dashRow{qrow("fix/rounding", claude.StateWaiting, 1)})
	a.dashboard.reply = replyState{active: true, path: "/wt/x", branch: "fix/rounding"}

	if !a.capturing() {
		t.Fatal("capturing() is false while the reply input is open")
	}

	for _, s := range []string{"m", "1", "5", "q", "?"} {
		m, _ := a.Update(key(s))
		a = m.(App)
		if a.quit {
			t.Fatalf("key %q quit the app while replying", s)
		}
		if a.current != ViewThreads {
			t.Fatalf("key %q switched tab while replying", s)
		}
		if a.showHelp {
			t.Fatalf("key %q opened help while replying", s)
		}
	}
	if a.dashboard.reply.text != "m15q?" {
		t.Errorf("text = %q, want the keys typed", a.dashboard.reply.text)
	}
}

// claude's own errors run long. A wrapped status line pushes the help row
// around under the table, which is how the "already running" error looked.
func TestReplyStatusStaysOneLine(t *testing.T) {
	d := Dashboard{width: 100, height: 40, rows: orderRows([]dashRow{qrow("fix/rounding", claude.StateWaiting, 1)})}
	d.reply.branch = "fix/rounding" // the line belongs to its thread
	d.reply.sent = "reply to fix/rounding failed: claude: " + strings.Repeat("a long explanation from claude ", 10)

	out := d.View()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "a long explanation") && len([]rune(line)) > d.width {
			t.Errorf("status line is %d runes wide, terminal is %d:\n%s", len([]rune(line)), d.width, line)
		}
	}
	if !strings.Contains(out, "…") {
		t.Errorf("long status was not truncated:\n%s", out)
	}
}

// The trap that dropped a user out of norn: i was refused on a non-waiting
// thread with a dim one-line status, so the keystrokes went to the dashboard
// instead, where enter means "cd into this worktree and quit".
func TestRefusedReplyNeverLeavesYouTypingAtTheDashboard(t *testing.T) {
	row := seedWaitingThread(t)
	row.AgentState = claude.StateWorking // nothing to answer yet

	a := App{current: ViewThreads, mainDir: "/repo"}
	a.dashboard.rows = orderRows([]dashRow{row})
	a.dashboard.cfg = config.Config{Agent: config.AgentConfig{Command: "claude"}}

	m, _ := a.Update(key("i"))
	a = m.(App)
	if !a.dashboard.reply.active {
		t.Fatal("i did not open the input on a working thread")
	}

	for _, s := range []string{"y", "e", "s"} {
		m, _ = a.Update(key(s))
		a = m.(App)
	}
	m, _ = a.Update(key("enter"))
	a = m.(App)

	if a.quit {
		t.Fatal("enter quit norn instead of refusing the reply")
	}
	if !strings.Contains(a.dashboard.reply.sent, "still working") {
		t.Errorf("refusal not explained: %q", a.dashboard.reply.sent)
	}
}

// The permission choice is per reply: "answer this" and "go finish it" are
// different asks, and only the second grants an unattended run authority.
// Named in norn's words, since every agent spells this differently.
func TestReplyGrantCyclesAndIsShown(t *testing.T) {
	if got := config.GrantAnswer.Next(); got != config.GrantEdit {
		t.Errorf("answer cycles to %q", got)
	}
	if got := config.GrantEdit.Next(); got != config.GrantAct {
		t.Errorf("edit cycles to %q", got)
	}
	if got := config.GrantAct.Next(); got != config.GrantAnswer {
		t.Errorf("act cycles to %q, want back to answer", got)
	}

	row := seedWaitingThread(t)
	a := App{current: ViewThreads}
	a.dashboard.rows = orderRows([]dashRow{row})
	a.dashboard.cfg = config.Config{Agent: config.AgentConfig{Command: "claude"}}

	m, _ := a.Update(key("i"))
	a = m.(App)
	if a.dashboard.reply.grant != config.GrantAnswer {
		t.Errorf("input opened at %q, want the weakest grant", a.dashboard.reply.grant)
	}
	if out := a.dashboard.View(); !strings.Contains(out, "answer only") {
		t.Errorf("grant not shown in the prompt:\n%s", out)
	}

	m, _ = a.Update(key("tab"))
	a = m.(App)
	if a.dashboard.reply.grant != config.GrantEdit {
		t.Errorf("tab did not cycle: %q", a.dashboard.reply.grant)
	}
	if out := a.dashboard.View(); !strings.Contains(out, "may edit files") {
		t.Errorf("cycled grant not shown:\n%s", out)
	}
	if a.current != ViewThreads {
		t.Error("tab switched tab while replying")
	}
}

// The config value is the starting point, not a lock.
func TestReplyModeStartsFromConfig(t *testing.T) {
	row := seedWaitingThread(t)
	a := App{current: ViewThreads}
	a.dashboard.rows = orderRows([]dashRow{row})
	a.dashboard.cfg = config.Config{
		Agent:               config.AgentConfig{Command: "claude"},
		ReplyPermissionMode: "act",
	}

	m, _ := a.Update(key("i"))
	a = m.(App)
	if a.dashboard.reply.grant != config.GrantAct {
		t.Errorf("grant = %q, want the configured act", a.dashboard.reply.grant)
	}
}

// A reply is one thread's business. Showing its spinner while you are looking
// at another thread says the wrong one is busy.
func TestReplyLineOnlyShowsOnItsOwnThread(t *testing.T) {
	rows := orderRows([]dashRow{
		qrow("fix/rounding", claude.StateWaiting, 1),
		qrow("feature/login", claude.StateWaiting, 2),
	})
	d := Dashboard{width: 100, height: 40, rows: rows}
	d.reply.branch, d.reply.pending = "fix/rounding", "yes please"

	for i, r := range d.visibleRows() {
		d.cursor = i
		out := d.View()
		onTarget := r.Branch == "fix/rounding"
		if shown := strings.Contains(out, "yes please"); shown != onTarget {
			t.Errorf("cursor on %s: pending line shown = %v, want %v", r.Branch, shown, onTarget)
		}
	}
}

// A headless role is never "waiting": it ran to completion and exited, and its
// live agent state reads idle. Judging it by that rule would make every
// finished role unanswerable, which is the whole point of being able to reply.
func TestCanReplyToAFinishedRole(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	const wt = "/wt/feature/x/logic"
	slug := strings.NewReplacer("/", "-", ".", "-").Replace(wt)
	dir := filepath.Join(home, "projects", slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	role := func(run string) dashRow {
		r := qrow("feature/x/logic", claude.StateIdle, 1)
		r.Path, r.WorktreeAlive = wt, true
		r.TaskID, r.Role, r.Run = "t1", "logic", run
		return r
	}
	for _, run := range []string{state.RunMerged, state.RunDone, state.RunFailed} {
		if !canReply(role(run)) {
			t.Errorf("a %s role cannot be answered: %q", run, replyRefusal(role(run)))
		}
	}
	if why := replyRefusal(role(state.RunRunning)); why == "" {
		t.Error("a running role was offered a reply, which would resume a session still in use")
	}

	// A thread that is not a role keeps the waiting-only rule.
	plain := qrow("fix/rounding", claude.StateIdle, 1)
	plain.Path, plain.WorktreeAlive = wt, true
	if canReply(plain) {
		t.Error("an idle non-role thread became answerable")
	}
}

// The exchange goes into the role's own run log, so the viewer holds both
// halves of the conversation and not only the part norn started.
func TestReplyLogRecordsBothSides(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	r := qrow("feature/x/logic", claude.StateIdle, 1)
	r.TaskID, r.Role = "t1", "logic"

	log := replyLogFor(r)
	if log.path == "" {
		t.Fatal("a role got no run log to record into")
	}
	if err := os.MkdirAll(filepath.Dir(log.path), 0o755); err != nil {
		t.Fatal(err)
	}
	log.append("you: rerun the tests")
	log.append("done, they pass")

	data, err := os.ReadFile(log.path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "you: rerun the tests\ndone, they pass\n" {
		t.Fatalf("run log = %q", got)
	}

	// A thread that is not a role has nowhere to record, and must not panic.
	replyLogFor(qrow("fix/rounding", claude.StateIdle, 1)).append("anything")
}

// The run log is a conversation, not a file view: every printable key types
// into it. A pane where `r` refreshes and `q` quits is a pane you cannot write
// "run the tests" in, which is the whole reason it has an input.
func TestRunLogPaneTypesInsteadOfActing(t *testing.T) {
	d := Dashboard{}
	d.rows = orderRows([]dashRow{roleRow()})
	d.showLog, d.logTask, d.logRole, d.logRow = true, "t1", "logic", roleRow()
	d.reply.active = true

	for _, s := range []string{"r", "q", "d", "R", "l"} {
		m, _ := d.Update(key(s))
		d = m.(Dashboard)
		if !d.showLog {
			t.Fatalf("key %q closed the conversation", s)
		}
		if d.quit {
			t.Fatalf("key %q quit from the conversation", s)
		}
	}
	if d.reply.text != "rqdRl" {
		t.Fatalf("text = %q, want the keys typed", d.reply.text)
	}

	// esc is the way out, and it leaves nothing armed behind it.
	m, _ := d.Update(key("esc"))
	d = m.(Dashboard)
	if d.showLog || d.reply.active || d.reply.text != "" {
		t.Fatalf("esc left the pane armed: showLog=%v active=%v text=%q", d.showLog, d.reply.active, d.reply.text)
	}
}

// roleRow is a finished role: what the conversation pane opens on.
func roleRow() dashRow {
	r := qrow("feature/x/logic", claude.StateIdle, 1)
	r.Path, r.WorktreeAlive = "/wt/feature/x/logic", true
	r.TaskID, r.Role, r.Run = "t1", "logic", state.RunMerged
	return r
}
