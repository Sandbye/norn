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
	"fmt"
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

// EnrichBranchName returns an AI-suggested branch name for the hint, or the
// given fallback if Claude fails or returns something invalid. Callers gate on
// Available() + config + git.BranchLacksSlug before calling.
func EnrichBranchName(ctx context.Context, dir, hint, fallback string) string {
	s, err := SuggestBranch(ctx, dir, hint)
	if err != nil {
		return fallback
	}
	if nb := git.NormalizeSuggestedBranch(s); nb != "" {
		return nb
	}
	return fallback
}

// SuggestBranch asks Claude for a Conventional Branch name for a task hint,
// resolving a ClickUp id/URL via the clickup MCP when present. Returns the raw
// suggestion (caller sanitises with git.NormalizeSuggestedBranch). Short timeout
// so a slow/hung lookup never stalls worktree creation for long.
func SuggestBranch(ctx context.Context, dir, hint string) (string, error) {
	prompt := "Name a git branch for this task. Hint: \"" + hint + "\". " +
		"If it's a ClickUp task id or URL, look it up via the clickup MCP for its title and list. " +
		"Output ONLY one line, no prose/quotes/backticks: a branch name of the form " +
		"<type>/#<clickup-id>/<slug> (drop the #<id> segment if there is no ClickUp id). " +
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

	args := []string{"-p", prompt, "--output-format", "json"}
	if len(opts.AllowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(opts.AllowedTools, ","))
	}
	if opts.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", opts.SystemPrompt)
	}

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = dir
	if opts.Stdin != "" {
		cmd.Stdin = strings.NewReader(opts.Stdin)
	}

	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return Result{}, fmt.Errorf("claude timed out after %s", timeout)
		}
		return Result{}, fmt.Errorf("claude -p failed: %w", err)
	}

	return parseEnvelope(out)
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
