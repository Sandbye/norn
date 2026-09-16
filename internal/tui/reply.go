package tui

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sandbye/norn/internal/claude"
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
	active bool
	text   string
	path   string // worktree being answered
	branch string // for the status line, since rows reorder under us
	sent   string // last outcome, shown until the next action
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

// replySentMsg reports the outcome of a headless reply.
type replySentMsg struct {
	branch string
	text   string // the agent's response, trimmed
	err    error
}

// replyCmd sends one line to a worktree's existing session. mode is passed to
// --permission-mode: empty keeps Claude Code's -p default, under which anything
// needing approval is denied rather than waiting for someone who isn't there.
func replyCmd(path, branch, text, mode string) tea.Cmd {
	return func() tea.Msg {
		res, err := claude.Reply(context.Background(), path, text, mode)
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

// canReply reports whether a row can be answered. Only a waiting thread: a
// working one has a live session that --continue will not attach to, and an
// idle or unknown one has nothing asking.
func canReply(r dashRow) bool {
	return r.WorktreeAlive && r.AgentState == claude.StateWaiting
}
