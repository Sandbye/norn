# work — roadmap

Derived from a 2026-06 landscape scan (worktree managers, agent orchestrators, Charm ecosystem, Claude Code 2.x). Filtered for a solo dev running one Claude session per worktree (~20 live). Ordered by leverage.

---

## ▶ Next: Headless Claude integration

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

## Backlog

### Tier 1 — high leverage, fits the setup
- [ ] **Per-worktree lifecycle hooks** in `.work.yaml`: `on_create` / `pre_merge` / `post_merge` (auto-install deps, copy configs, run linter on spawn). Extends existing `verify`/`setup`.
- [ ] **Build-artifact sharing across worktrees** — symlink/share heavy dirs (`node_modules`, `dist`, `target`) on create. Cuts disk + cold-start at ~20 worktrees.
- [ ] **Port/env collision detection** — worktrees share the filesystem; two `pnpm dev` clash. Detect + auto-assign port ranges per worktree.

### Tier 2 — nice, lower urgency
- [ ] **`huh` forms** for the create flow — validated, themed hint+base picker (Charm, Bubble Tea v2 native). https://github.com/charmbracelet/huh
- [ ] **`VHS` demo GIFs** — scripted terminal recordings for the README / Homebrew tap. https://github.com/charmbracelet/vhs
- [ ] **Bubble Tea v2 "cursed renderer"** — faster, flicker-free dashboard re-renders. Adopt whenever rendering is next touched. https://charm.land/blog/v2/
- [ ] **Conflict/risk surfacing in diff view** — flag when two live worktrees touch the same file/function before merge.

### Tier 3 — deliberately skipped (over-engineering for solo)
- Multi-agent "teams" / peer-to-peer agent messaging / shared task lists with auto-dependency resolution. These target *teams of agents coordinating on one feature*; not the one-dev-one-agent-per-task pattern. Revisit only if running 5+ parallel agents on a single feature.

### Pre-existing (from earlier robustness pass, still open)
- [ ] State-file lock (`state.Mutate` + flock) — concurrent `work` writes can lose session rows.
- [ ] Git-op timeouts (context) — bad network shouldn't hang the TUI.
- [ ] Detached-HEAD detection in `currentBranch`/`branchAt`.
- [ ] `.work.yaml` committed into AirwalletDashboard (team-shareable config).
- [ ] Wire `text/template` for prompt generation (templates exist, unwired).

---

## Sources
- Claude headless: https://code.claude.com/docs/en/headless
- Charm v2: https://charm.land/blog/v2/ · huh · vhs (GitHub)
- Worktree-tool landscape (2026): nimbalyst.com, gwq, workmux — patterns only, not endorsements
