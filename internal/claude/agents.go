package claude

// Live sessions, as Claude Code itself reports them.
//
// `claude agents --json` is a supported machine-readable listing ("for
// scripting; does not require a TTY"), keyed by the session's cwd, which is how
// norn identifies a worktree. It knows two things the transcript cannot:
// whether a session is held by the daemon or bound to a terminal, and whether
// it is blocked on the user. Transcript tailing infers the second from a turn
// ending, which is why a thread could read "waiting" here while Claude Code
// considered it running and refused to continue it.

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"time"
)

// agentsTimeout bounds the listing. It is a local daemon query, so this is a
// guard against a wedged daemon, not a budget.
const agentsTimeout = 10 * time.Second

// Session is one live Claude Code session.
type Session struct {
	ID        string `json:"id"`  // short id, what `claude attach`/`stop` take
	PID       int    `json:"pid"` // interactive sessions only
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	Kind      string `json:"kind"`   // background | interactive
	Name      string `json:"name"`   // human title Claude Code gave the session
	State     string `json:"state"`  // background: blocked | …
	Status    string `json:"status"` // interactive: idle | …
}

// Background reports whether the daemon holds this session, which is what makes
// it attachable and stoppable rather than tied to somebody's terminal.
func (s Session) Background() bool { return s.Kind == "background" }

// Blocked reports whether the session is waiting on the user. Claude Code says
// so directly here, rather than norn inferring it from a stop_reason.
func (s Session) Blocked() bool { return s.State == "blocked" }

// Sessions lists Claude Code's live sessions, newest first. Returns nil when
// the CLI is missing or the daemon is unreachable: no listing is "unknown", the
// same degrade path as an unreadable transcript.
func Sessions(ctx context.Context) []Session {
	ctx, cancel := context.WithTimeout(ctx, agentsTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, "claude", "agents", "--json").Output()
	if err != nil {
		return nil
	}
	var sessions []Session
	if json.Unmarshal(out, &sessions) != nil {
		return nil
	}
	return sessions
}

// SessionFor returns the live session for a worktree, or false. Paths are
// compared through EvalSymlinks: on macOS a worktree under /tmp reaches the
// same directory as /private/tmp, and a string compare would miss.
func SessionFor(sessions []Session, worktreePath string) (Session, bool) {
	want := resolvePath(worktreePath)
	for _, s := range sessions {
		if resolvePath(s.Cwd) == want {
			return s, true
		}
	}
	return Session{}, false
}

func resolvePath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return p
}
