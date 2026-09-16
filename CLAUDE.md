# norn — Git Worktree Manager TUI

Go TUI for managing git worktrees with per-worktree coding-agent sessions. Replaces the previous bash script at `~/.local/bin/work`.

## Conventions

Conventions for contributing:

- **Commits** — Conventional Commits: `type(scope): imperative`, title-only by default, one change per title, body only when the *why* isn't obvious.
- **Branches** — Conventional Branch `<type>/<title>` (feature|fix|chore|…).
- **PRs** — concise; no test-plan section unless asked.
- **Style** — direct, terse; match surrounding Go; simple over clever.

This file documents the *codebase* (architecture, design decisions, roadmap), not collaboration rules.

## Architecture

```
cmd/norn/main.go           Entry point, CLI arg routing, doctor, shell-init
cmd/norn/brief.go          `norn brief` — headless JSON (branch, brief, config), creates nothing
internal/
  paths/paths.go            Config/cache/state dir resolution + the "work" legacy fallback
  config/config.go          YAML config loading (global + shared repo + personal per-project)
  config/edit.go            Comment-preserving YAML writeback for the Settings tab
  git/git.go                Git operations (worktree CRUD, remote checks, branch utils), all timeout-bounded
  state/state.go            sessions.json store; state/lock.go adds flock'd Mutate
  claude/claude.go          Headless `claude -p` runs; claude/session.go probes live agent state
  task/task.go              Tracker task lookup (github | clickup)
  tui/
    app.go                  Bubble Tea app, tabbed view routing, commands
    dashboard.go            Threads tab — live worktree table with STATE/NEXT
    clean.go                Clean view — age, remote status, multi-select delete
    create.go               Create view — hint input + base branch picker
    diff.go                 Diff viewer + review flow (PR and local sinks)
    settings.go             Settings tab — layered config editing
    styles.go               Lip Gloss styles, Nord + frog palettes
    helpers.go              Agent launch, prompt generation, file utils
  prompt/
    prompt.go               Template rendering (text/template): Render, Resolve, List, NewTemplate
    templates/*.md.tmpl     Built-in templates (task, review, checkout); user overrides in ~/.config/norn/templates
  worktree/worktree.go      What one create produces: a single worktree, or a trunk + one per role
  review/review.go          Local review model + .norn/review.md rendering (conventional comments)
```

## Stack

- **Go** + **Bubble Tea** (TUI framework) + **Lip Gloss** (styling)
- **Nord** color scheme (plus a `frog` palette)
- Config: `~/.config/norn/config.yaml` (global) + `.norn.yaml` (per-repo, committed) + `~/.config/norn/projects/<repo>.yaml` (personal)

## Config

Global config at `~/.config/norn/config.yaml`:
```yaml
worktree_dir: ~/worktrees
user:
  name: Your Name
  email: you@example.com
base_branches: [main, master]
```

Per-project config at `<repo-root>/.norn.yaml` (overrides global):
```yaml
clickup:
  lists:
    backlog: "123456789012"
    bugs: "123456789013"
verify:
  - pnpm check-types
  - pnpm lint
setup: pnpm install
base_branches: [main, develop]
```

## Key Design Decisions

1. **Composable config** — global defaults + per-project overrides. No hardcoded repo-specific stuff in the binary.
2. **Remote branch check in clean** — `git fetch --prune` then checks if `origin/<branch>` exists. Shows "gone" / "active" per worktree.
3. **Age from last commit** — not filesystem mtime. More meaningful for staleness.
4. **Direct create shortcut** — `norn create "hint"` skips the TUI entirely for fast worktree creation (picks first base branch).
5. **ExecProcess for Claude** — Bubble Tea hands off terminal control to Claude via `tea.ExecProcess`. Alt screen exits cleanly.
6. **Prompt generation via `text/template`** — `internal/prompt` renders `.worktree.md` from config + the selected template. Precedence: `--template` flag → `cfg.template` (task) → kind. User overrides live in `~/.config/norn/templates` (or `templates.dir`); `norn template new <name>` scaffolds one.
7. **One review flow, two sinks** — the diff view's comment/review machinery is shared: PR mode POSTs to GitHub, local mode writes `.norn/review.md` and offers to resume the agent with it. Comments carry a conventional-comment label + blocking flag, rendered into the body for both sinks.

## Current State

Released through v0.9.x; the backlog lives in GitHub Issues and milestones. What
the binary does is documented in `README.md` — don't duplicate a feature list
here, it goes stale.

Two invariants worth keeping in mind when touching the internals:

- **Paths go through `internal/paths`.** Nothing else joins `.config`/`.cache`/
  `.local/state` by hand, because that package also owns the fallback to the
  pre-rename `work` namespace.
- **Session-store writes go through `state.Mutate`.** A bare `Load` + `Save`
  pair races other norn processes and silently drops rows.

## Next Steps

See `ROADMAP.md` for the current backlog (shipped items + Tier 1-3). It's the single source of truth; this file documents architecture, not the todo list.

## Build & Install

```bash
cd ~/Documents/GitHub/norn
go build ./cmd/norn/       # build
go install ./cmd/norn/     # install to ~/go/bin/norn
```

Binary goes to `~/go/bin/norn` (ensure `~/go/bin` is on PATH).
