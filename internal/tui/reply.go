package tui

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sandbye/norn/internal/claude"
	"github.com/sandbye/norn/internal/config"
)

// Answering a waiting thread from the dashboard. Most waiting threads need one
// word (yes, the second option, go ahead), and paying a full context switch for
// one word is the cost this exists to remove: no cd, no session load, no
// re-reading what the thread asked.
//
// The reply goes through `claude -p --continue`, so it never takes the
// terminal. Claude Code resumes a finished session but not a running one, which
// is why this is only offered on a thread that is waiting.

// replyState is the one-line input. Deliberately not bubbles/textinput: this is
// a single line with no cursor movement, and the filter input next to it works
// the same way.
type replyState struct {
	active  bool
	text    string
	path    string       // worktree being answered
	branch  string       // for the status line, since rows reorder under us
	target  dashRow      // the thread as it was when the input opened
	grant   config.Grant // what this reply may do, cycled with tab
	pending string       // what was sent, while the run is still going
	sent    string       // last outcome, shown until the next action
}

// handleKey edits the pending reply and reports whether it consumed the key.
// Enter and esc are left to the caller: one sends, the other cancels, and both
// need the model.
func (r *replyState) handleKey(s string) bool {
	if !r.active {
		return false
	}
	switch s {
	case "backspace", "ctrl+h":
		if n := len(r.text); n > 0 {
			runes := []rune(r.text)
			r.text = string(runes[:len(runes)-1])
		}
		return true
	case "ctrl+u":
		r.text = ""
		return true
	default:
		if len(s) == 1 && s[0] >= 0x20 && s[0] < 0x7f {
			r.text += s
			return true
		}
	}
	return false
}

// onReplyTarget reports whether the cursor is on the thread a reply belongs to.
// The pending and outcome lines are that thread's state, so showing them while
// you are looking at a different one says the wrong thread is busy.
func (d Dashboard) onReplyTarget(vis []dashRow) bool {
	if d.reply.branch == "" {
		return false
	}
	return d.cursor >= 0 && d.cursor < len(vis) && vis[d.cursor].Branch == d.reply.branch
}

// replySentMsg reports the outcome of a headless reply.
type replySentMsg struct {
	branch string
	text   string // the agent's response, trimmed
	err    error
}

// replyCmd sends one line to a worktree's existing session. mode is passed to
// --permission-mode: empty keeps Claude Code's -p default, under which anything
// needing approval is denied rather than waiting for someone who isn't there.
func replyCmd(path, branch, text string, grant config.Grant) tea.Cmd {
	return func() tea.Msg {
		// Address the session by id. --continue guesses "most recent" and
		// refuses outright while the daemon holds the session, which is the
		// normal state for a thread you left open.
		var live *claude.Session
		if s, ok := claude.SessionFor(claude.Sessions(context.Background()), path); ok {
			live = &s
		}
		res, err := claude.ReplyTo(context.Background(), path, live, text, claude.PermissionModeFor(string(grant)))
		if err != nil {
			return replySentMsg{branch: branch, err: err}
		}
		if res.IsError {
			return replySentMsg{branch: branch, text: res.Text, err: errReplyRefused}
		}
		return replySentMsg{branch: branch, text: strings.TrimSpace(res.Text)}
	}
}

// errReplyRefused marks a run that completed but reported a failure, which is
// how a denied tool call comes back: exit 0, is_error true.
var errReplyRefused = errReply("the agent reported an error (a tool it needed may have been denied)")

type errReply string

func (e errReply) Error() string { return string(e) }

// canReply reports whether a row can be answered: a waiting thread with a
// transcript to resume.
//
// The transcript check is not redundant. `claude -p --continue` does not fail
// when there is nothing to continue: it starts a fresh conversation and exits
// 0, so the answer would land in a session that never saw the question, and
// still bill for it. Waiting state already implies a transcript, so this only
// costs a stat, and it makes the guard the code's rather than the flag's.
//
// Whether the session is daemon-held is deliberately not checked here: that
// costs a subprocess per row per tick, and replyCmd has to look it up anyway.
// replyRefusal explains why a thread cannot be answered, or "" when it can.
// Checked when the reply is sent rather than when the input opens, so a
// keystroke is never silently handed back to the dashboard.
func replyRefusal(r dashRow) string {
	switch {
	case !r.WorktreeAlive:
		return "that worktree is gone"
	case !claude.HasSession(r.Path):
		return "no session to answer in this worktree"
	case r.AgentState == claude.StateWorking:
		return "that thread is still working, so there is nothing to answer yet"
	case r.AgentState != claude.StateWaiting:
		return "only a waiting thread can be answered"
	}
	return ""
}

func canReply(r dashRow) bool {
	return r.WorktreeAlive && r.AgentState == claude.StateWaiting && claude.HasSession(r.Path)
}

// quoteOneLine renders sent text for a one-line status: newlines collapsed,
// long text cut, so a pasted paragraph can't reflow the dashboard.
func quoteOneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 60 {
		s = string(r[:59]) + "…"
	}
	return `"` + s + `"`
}
