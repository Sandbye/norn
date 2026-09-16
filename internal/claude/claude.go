// Package claude drives Claude Code non-interactively (`claude -p`) so the TUI
// can summarize sessions, draft PR bodies, etc. without opening a session.
//
// Auth note: we intentionally do NOT pass --bare. Bare mode skips OAuth/keychain
// and requires ANTHROPIC_API_KEY; plain `claude -p` uses the normal login, so it
// works for subscription users without an API key.
package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sandbye/norn/internal/config"
	"os/exec"
	"strings"
	"time"

	"github.com/sandbye/norn/internal/git"
)

// Result is the parsed envelope from `claude -p --output-format json`.
type Result struct {
	Text      string
	SessionID string
	CostUSD   float64
	IsError   bool
}

// Options tunes a headless run. Zero value is a sane read-only call.
type Options struct {
	AllowedTools []string      // joined into a single --allowedTools value
	SystemPrompt string        // --append-system-prompt
	Stdin        string        // piped context (e.g. a diff)
	Timeout      time.Duration // 0 → 90s default
	// Resume names a specific session (--resume <id>). Preferred over Continue
	// when the id is known, since --continue guesses "most recent" and silently
	// starts a new conversation when it guesses wrong.
	Resume string
	// Continue resumes the directory's existing session (--continue) instead of
	// starting a fresh one. Claude Code will resume a session that has finished
	// but not one still running, so callers must only set this for a thread
	// that is waiting.
	Continue bool
	// PermissionMode is --permission-mode. Empty means Claude Code's default for
	// -p, which is Manual: anything that would prompt is denied, because an
	// unattended run has nobody to ask. Callers translate a norn grant into
	// this with PermissionModeFor.
	PermissionMode string
}

// envelope mirrors the JSON shape documented at code.claude.com/docs/en/headless.
type envelope struct {
	Result    string  `json:"result"`
	SessionID string  `json:"session_id"`
	TotalCost float64 `json:"total_cost_usd"`
	IsError   bool    `json:"is_error"`
	Subtype   string  `json:"subtype"`
}

// Available reports whether the `claude` binary is on PATH.
func Available() bool {
	_, err := exec.LookPath("claude")
	return err == nil
}

// EnrichBranchName returns an AI-named branch for the hint, composed in the
// repo's branch_format, or the given fallback if Claude fails or returns
// something invalid. Claude only supplies type + slug; the id and its placement
// stay with git.ComposeBranch so the format lives in one place. Callers gate on
// Available() + config + git.BranchLacksSlug before calling.
func EnrichBranchName(ctx context.Context, dir, hint, fallback, format string) string {
	s, err := SuggestBranch(ctx, dir, hint)
	if err != nil {
		return fallback
	}
	prefix, title := git.SuggestedBranchParts(s)
	if prefix == "" || title == "" {
		return fallback
	}
	return git.ComposeBranch(format, prefix, git.ClickUpID(hint), title)
}

// SuggestBranch asks Claude for a branch type and slug for a task hint,
// resolving a ClickUp id/URL via the clickup MCP when present. Returns the raw
// suggestion (caller parses with git.SuggestedBranchParts). Short timeout so a
// slow/hung lookup never stalls worktree creation for long.
func SuggestBranch(ctx context.Context, dir, hint string) (string, error) {
	prompt := "Name a git branch for this task. Hint: \"" + hint + "\". " +
		"If it's a ClickUp task id or URL, look it up via the clickup MCP for its title and list. " +
		"Output ONLY one line, no prose/quotes/backticks: <type>/<slug>. " +
		"Do NOT include the ClickUp id — it is added afterwards. " +
		"type is one of feature|fix|hotfix|epic|chore: fix for bugs/operations, feature for new features, " +
		"chore for refactor/docs/deps, hotfix for urgent production fixes, epic for umbrella tasks. " +
		"slug is 3-6 words, lowercase kebab-case, describing the task."
	res, err := Run(ctx, dir, prompt, Options{
		Timeout: 45 * time.Second,
		AllowedTools: []string{
			"mcp__clickup__clickup_get_task",
			"mcp__clickup__clickup_search",
			"mcp__clickup__clickup_filter_tasks",
		},
	})
	if err != nil {
		return "", err
	}
	if res.IsError {
		return "", fmt.Errorf("claude reported an error")
	}
	return res.Text, nil
}

// Run executes `claude -p <prompt>` in dir and returns the parsed result.
func Run(ctx context.Context, dir, prompt string, opts Options) (Result, error) {
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 90 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "claude", buildArgs(prompt, opts)...)
	cmd.Dir = dir
	if opts.Stdin != "" {
		cmd.Stdin = strings.NewReader(opts.Stdin)
	}

	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return Result{}, fmt.Errorf("claude timed out after %s", timeout)
		}
		// claude puts the reason on stderr, and an exit status alone is not
		// diagnosable: "already running", "not logged in" and "no session to
		// continue" all surface as exit 1.
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if msg := strings.TrimSpace(string(ee.Stderr)); msg != "" {
				return Result{}, fmt.Errorf("claude: %s", firstLine(msg))
			}
		}
		return Result{}, fmt.Errorf("claude -p failed: %w", err)
	}

	return parseEnvelope(out)
}

// buildArgs assembles the CLI flags for a headless run. Split out from Run so
// the flag logic is testable: a missing --continue silently starts a new
// session instead of answering the one on screen, which no exit code reveals.
func buildArgs(prompt string, opts Options) []string {
	args := []string{"-p", prompt, "--output-format", "json"}
	if opts.Resume != "" {
		args = append(args, "--resume", opts.Resume)
	} else if opts.Continue {
		args = append(args, "--continue")
	}
	if opts.PermissionMode != "" {
		args = append(args, "--permission-mode", opts.PermissionMode)
	}
	if len(opts.AllowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(opts.AllowedTools, ","))
	}
	if opts.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", opts.SystemPrompt)
	}
	return args
}

// ReplyTimeout is the budget for an inline reply. Generous on purpose: a reply
// restarts real work, and killing it halfway leaves the thread mid-turn.
const ReplyTimeout = 15 * time.Minute

// PermissionModeFor translates a norn grant into Claude Code's own flag value.
// Manual is the empty string, which is already -p's default, so "answer" passes
// no flag at all.
//
//	answer → Manual       anything needing approval is denied
//	edit   → acceptEdits  writes files; shell still needs an allow rule
//	act    → auto         a classifier reviews each action instead of you
func PermissionModeFor(grant string) string {
	switch config.Grant(grant) {
	case config.GrantEdit:
		return "acceptEdits"
	case config.GrantAct:
		return "auto"
	}
	return ""
}

// Stop ends a background session so it can be resumed headlessly. Claude Code
// refuses to resume a session the daemon is still holding, and says so, naming
// this as the way through.
func Stop(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, agentsTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "claude", "stop", id).CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("claude stop %s: %s", id, firstLine(msg))
		}
		return fmt.Errorf("claude stop %s: %w", id, err)
	}
	return nil
}

// ReplyTo sends one line to a worktree's session, stopping it first when the
// daemon holds it. Addressing the session by id rather than by --continue is
// what makes the reply land in the thread on screen instead of a new
// conversation.
//
// live may be absent: a headless reply ends the run, so the daemon stops
// holding the session and it leaves `claude agents` even though the
// conversation is still resumable. The transcript keeps the id either way.
func ReplyTo(ctx context.Context, dir string, live *Session, text, mode string) (Result, error) {
	id := SessionIDFor(dir)
	if live != nil {
		if live.Background() {
			if err := Stop(ctx, live.ID); err != nil {
				return Result{}, err
			}
		}
		if live.SessionID != "" {
			id = live.SessionID
		}
	}
	if id == "" {
		return Result{}, fmt.Errorf("no session to resume for %s", dir)
	}
	return Run(ctx, dir, text, Options{
		Resume:         id,
		PermissionMode: mode,
		Timeout:        ReplyTimeout,
	})
}

// Reply continues the worktree's session with one line from the user, headless,
// so the dashboard can answer a waiting thread without taking over the
// terminal. mode is passed through to --permission-mode; empty keeps Claude
// Code's -p default, under which anything needing approval is denied and the
// agent is told so rather than stalling.
func Reply(ctx context.Context, dir, text, mode string) (Result, error) {
	return Run(ctx, dir, text, Options{
		Continue:       true,
		PermissionMode: mode,
		Timeout:        ReplyTimeout,
	})
}

// firstLine keeps a status line to one line: claude's stderr can run long, and
// this lands in a dashboard footer.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

func parseEnvelope(out []byte) (Result, error) {
	var e envelope
	if err := json.Unmarshal(out, &e); err != nil {
		return Result{}, fmt.Errorf("parse claude json: %w", err)
	}
	return Result{
		Text:      strings.TrimSpace(e.Result),
		SessionID: e.SessionID,
		CostUSD:   e.TotalCost,
		IsError:   e.IsError,
	}, nil
}
