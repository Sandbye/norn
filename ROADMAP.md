# norn — roadmap

Derived from a 2026-06 landscape scan (worktree managers, agent orchestrators, Charm ecosystem, Claude Code 2.x), plus the mid-2026 OSS/UX push. Filtered for a solo dev running one agent session per worktree (~20 live). Ordered by leverage.

---

## ✓ Shipped

- **Headless Claude integration** — `internal/claude` (`Run`, `Available`), dashboard "summarize" (`s`), AI branch naming. Gated to the `claude` agent.
- **PR review-since-stamp diff** — `norn diff <pr#> --since-review` overlays your own (even outdated) comments next to current code; split view.
- **Local code review** — comment any local diff with conventional-comment labels (`c`, `v` range, `C` file), `R` writes `.norn/review.md` and resumes the agent against it. No PR, no public repo. Same keys still post a real review in PR mode.
- **Rebrand `work` → `norn`** — module, GitHub repo, Nord theme, "many threads, one tree" identity, MIT license, README.
- **Configurable agent** — `agent.command` (claude/opencode/…); headless features degrade for non-claude.
- **Named templates** — `--template`, `norn --templates`, per-project override dir, `template:` default.
- **Settings TUI** — comment-preserving yaml writeback (`internal/config/edit.go`) + `$EDITOR` escape.
- **Unified tabbed program** — Threads · New · Clean · Settings in one process; retired the standalone menu.
- **Nord-framed centered views** — sealed background gaps (opencode technique), `?`-collapsible help, `o` opens the agent.
- **Theme system** — `nord` + `frog` palettes, Settings theme picker (live repaint), per-theme dashboard avatar (🐸 for frog).
- **`shell-init`** — drift-proof cd integration (`eval "$(norn shell-init zsh)"`) + auto-reap of stale cd-target files.
- **Session-log reconcile** — dashboard reflects live worktrees; dead/duplicate/main-checkout rows auto-pruned.
- **Detached-HEAD guard** + **`text/template` prompt rendering** (both were open in the earlier robustness pass).
- **Public launch (v0.1.0)** — VHS demo GIF + leaner README, Homebrew **cask** + tag-triggered release workflow, public repo + tap.
- **CLI polish** — `--version`, explicit `norn create` verb, per-session model pick (`M`) + `agent.model`, `m` go-to-main, constant frame height, `$EDITOR` arg handling.
- **`norn brief`** — headless JSON (branch name, rendered brief, resolved project config) for `--repo <checkout|worktree|bare mirror> --issue <n>|--hint <text>`. Creates nothing; lets external tools reuse norn's naming + policy instead of reimplementing them.

---

## Backlog

The live backlog is tracked as **[GitHub Issues](https://github.com/Sandbye/norn/issues)**, grouped by `area/*` labels and **[Milestones](https://github.com/Sandbye/norn/milestones)** (a milestone = a release). New here? See **[good first issues](https://github.com/Sandbye/norn/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22)** (themes, templates, small polish).

**Deliberately out of scope** (over-engineering for a solo, one-agent-per-worktree setup): multi-agent "teams", peer-to-peer agent messaging, shared task lists with auto-dependency resolution. Those target *teams of agents coordinating on one feature*. Revisit only if running 5+ parallel agents on a single feature.

---

## (shipped) Headless Claude integration

Turn `work` from a *launcher* into an *orchestrator* — drive Claude non-interactively from the TUI and render results inline, without opening a session.

### Verified interface (Claude Code 2.x, `claude -p`)

- `claude -p "<prompt>"` — non-interactive. Reads stdin (≤10MB), so diffs can be piped.
- `--bare` — skips auto-discovery of hooks/skills/MCP/CLAUDE.md for fast, deterministic, machine-identical runs. **Use this for `work`'s calls** and pass context explicitly. (Will become the `-p` default in a future release.)
- `--output-format json` → payload includes `.result` (text), `.session_id`, `.total_cost_usd`, usage metadata. `--json-schema '<schema>'` → typed result in `.structured_output`.
- `--output-format stream-json --verbose --include-partial-messages` → newline-delimited token stream (for live display, later).
- `--allowedTools "Read,Bash(git diff *)"` — auto-approve specific tools (prefix match needs the space before `*`). Or `--permission-mode dontAsk` for locked-down read-only.
- `--append-system-prompt "<text>"` — inject voice/role without replacing the default prompt.
- `--resume <session_id>` / `--continue` — continue a run; scoped to the project dir + its worktrees.
- Bare mode needs `ANTHROPIC_API_KEY` (no keychain/OAuth). Non-bare uses the normal login.

Docs: https://code.claude.com/docs/en/headless

### Design

New package `internal/claude/`:

```go
type Result struct {
    Text      string
    SessionID string
    CostUSD   float64
    IsError   bool
}
type Options struct {
    Bare         bool
    AllowedTools []string
    SystemPrompt string
    Stdin        string        // piped context (e.g. a diff)
    Schema       string        // optional --json-schema
    Timeout      time.Duration
}
func Run(ctx context.Context, dir, prompt string, opts Options) (Result, error)
```

- Shells out with `cmd.Dir = dir`, parses the JSON envelope.
- Driven from the TUI as a `tea.Cmd` (goroutine) so the loop never blocks; spinner while pending; result delivered as a `tea.Msg`.
- `context.WithTimeout` (reuse the planned git-op timeout pattern) — headless can be slow even with `--bare`.

### Phased use cases

1. **MVP — "summarize session" from the dashboard.** Key on a row → `claude --bare -p "summarize the work on this branch" --allowedTools "Read,Bash(git log *),Bash(git diff *)"` in the worktree → render in an overlay panel. Proves the integration end-to-end, read-only, low risk.
2. **PR body generation.** `git diff <base>...HEAD | claude --bare -p "<append-system-prompt: voice.md §1 PR style> write a concise PR body" --output-format json` → feed `open-pr` / `pbcopy`.
3. **Auto-draft `.worktree.md`** on create — break the hint into a task outline.
4. **Risk/conflict note** — summarize what a diff touches vs other live worktrees (pairs with Tier-2 conflict surfacing).

### Concerns / gates

- **Cost** — `.total_cost_usd` per call; surface it. Verify against plan: research suggests headless/SDK calls may bill from a separate credit pool (mid-2026) — confirm before heavy automation.
- **Permissions** — keep `--allowedTools` minimal (read + git-read) so no prompts; never `--dangerously-skip-permissions`.
- **`claude` presence** — add a `work doctor` check.
- **Verify**: unit test JSON-envelope parsing on a captured sample; manual run of summarize on a real worktree.

---

## (shipped) PR review-since-stamp diff (outdated-comment fix)

**Problem.** GitHub drops a reviewer's comment from the Files-changed diff once the author edits that line ("outdated") — you can't see your comment and the new code in one view, even viewing all commits. Confirmed GitHub limitation (community #23138).

**Insight.** `work diff <pr#>` already has the viewer, commit-scope picker, ref-diffing, syntax/wrap/jump/mouse. The fix is NOT side-by-side (expensive 2-column rewrite, and not the win). If BASE = your review-stamp commit and you diff `reviewSHA..HEAD` in the existing unified viewer, the old-side lines are the code as you reviewed it and the added lines below show how it was addressed — comment + new code in one scroll once comments are overlaid.

**MVP.** `work diff <pr#> --since-review` (and/or a key in the PR view):

1. **Resolve review-stamp SHA** — `gh api repos/{o}/{r}/pulls/{n}/reviews`, filter `user.login == me` (resolve me via `gh api user` → `.login`), latest by `submitted_at`, take `.commit_id` = BASE. HEAD = current branch tip.
2. **Diff `reviewSHA..HEAD`** — reuse the existing diff plumbing with BASE override (same path as `--base`). Two-dot == three-dot here since HEAD descends from reviewSHA.
3. **Fetch my comments** — `gh api .../pulls/{n}/comments --paginate`, filter to me; keep `path`, `original_line`, `body`, `in_reply_to_id`. (New: current code only *posts* comments, never *reads* existing.)
4. **Overlay inline** — render each comment anchored to its `original_line` on the old side, visually distinct from pending comments. New render hook in `diff.go`.

**Reuses:** the whole PR diff TUI. **New code:** reviews/comments `gh api` fetch + parse (`cmd/work/main.go`), an `ExistingComment` type + inline render in `internal/tui/diff.go`, and a flag/keybind to enter the mode.

**Concerns / gates.**
- If I have no review on the PR → fall back to base...HEAD with a note.
- Comments on files not in the `reviewSHA..HEAD` diff (commented line untouched since) — still surface them (a "comments with no remaining diff" tail) so nothing's hidden.
- `original_line` can be null on very old comments — fall back to `original_position` or list under the file.
- **Verify:** run against a real PR where I left a comment that went outdated; confirm the comment renders next to the current code.

**Deferred:** true side-by-side old|new rendering — only if unified+overlay proves insufficient.

---

## Sources
- Claude headless: https://code.claude.com/docs/en/headless
- Charm v2: https://charm.land/blog/v2/ · huh · vhs (GitHub)
- Worktree-tool landscape (2026): nimbalyst.com, gwq, workmux — patterns only, not endorsements
