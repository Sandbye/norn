# work — Git Worktree Manager TUI

Go TUI for managing git worktrees with Claude Code sessions. Replaces the previous bash script at `~/.local/bin/work`.

## Conventions

Conventions for contributing:

- **Commits** — Conventional Commits: `type(scope): imperative`, title-only by default, one change per title, body only when the *why* isn't obvious.
- **Branches** — Conventional Branch `<type>/<title>` (feature|fix|chore|…).
- **PRs** — concise; no test-plan section unless asked.
- **Style** — direct, terse; match surrounding Go; simple over clever.

This file documents the *codebase* (architecture, design decisions, roadmap), not collaboration rules.

## Architecture

```
cmd/work/main.go           Entry point, CLI arg routing
cmd/norn/brief.go          `norn brief` — headless JSON (branch, brief, config), creates nothing
internal/
  config/config.go          YAML config loading (global + per-project)
  git/git.go                Git operations (worktree CRUD, remote checks, branch utils)
  tui/
    app.go                  Bubble Tea app, view routing, commands
    menu.go                 Main menu — resume existing or create new
    clean.go                Clean view — table with age, remote status, multi-select delete
    create.go               Create view — hint input + base branch picker
    styles.go               Lip Gloss styles, Catppuccin Mocha palette
    helpers.go              Claude launch, prompt generation, file utils
  prompt/
    prompt.go               Template rendering (text/template): Render, Resolve, List, NewTemplate
    templates/*.md.tmpl     Built-in templates (task, review, checkout); user overrides in ~/.config/work/templates
  review/review.go          Local review model + .norn/review.md rendering (conventional comments)
```

## Stack

- **Go** + **Bubble Tea** (TUI framework) + **Lip Gloss** (styling)
- **Catppuccin Mocha** color scheme
- Config: `~/.config/work/config.yaml` (global) + `.work.yaml` (per-repo)

## Config

Global config at `~/.config/work/config.yaml`:
```yaml
worktree_dir: ~/worktrees
user:
  name: Your Name
  email: you@example.com
base_branches: [main, master]
```

Per-project config at `<repo-root>/.work.yaml` (overrides global):
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
4. **Direct create shortcut** — `work "hint"` skips TUI entirely for fast worktree creation (picks first base branch).
5. **ExecProcess for Claude** — Bubble Tea hands off terminal control to Claude via `tea.ExecProcess`. Alt screen exits cleanly.
6. **Prompt generation via `text/template`** — `internal/prompt` renders `.worktree.md` from config + the selected template. Precedence: `--template` flag → `cfg.template` (task) → kind. User overrides live in `~/.config/work/templates` (or `templates.dir`); `norn template new <name>` scaffolds one.
7. **One review flow, two sinks** — the diff view's comment/review machinery is shared: PR mode POSTs to GitHub, local mode writes `.norn/review.md` and offers to resume the agent with it. Comments carry a conventional-comment label + blocking flag, rendered into the body for both sinks.

## Current State (v0.1)

Working:
- [x] Build and install (`go install ./cmd/work/`)
- [x] `work --help`, `work --list`, `work --status`
- [x] TUI menu with resume/new/clean navigation
- [x] Clean view with age, remote status, multi-select, confirm
- [x] Create view with hint input and base branch picker
- [x] Direct create: `work "hint"`, `work --review "hint"`
- [x] Config loading from `~/.config/work/config.yaml`
- [x] .env symlink from main repo

## Next Steps

See `ROADMAP.md` for the current backlog (shipped items + Tier 1-3). It's the single source of truth; this file documents architecture, not the todo list.

## Build & Install

```bash
cd ~/Documents/GitHub/work
go build ./cmd/norn/       # build
go install ./cmd/norn/     # install to ~/go/bin/norn
```

Binary goes to `~/go/bin/norn` (ensure `~/go/bin` is on PATH).
