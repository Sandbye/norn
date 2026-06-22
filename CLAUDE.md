# work — Git Worktree Manager TUI

Go TUI for managing git worktrees with Claude Code sessions. Replaces the previous bash script at `~/.local/bin/work`.

## Conventions

This repo defers to the global profile (`~/.claude/CLAUDE.md`, which imports `~/Documents/GitHub/profile/*`). Don't restate those rules here — follow them:

- **Commits** — Conventional Commits in sandbye's voice: title-only by default, concise, one change per title, body only when the *why* is non-obvious. Spec: `profile/dev/preferences.md` "Commits" + `profile/voice.md` §6.
- **Branches** — Conventional Branch `<type>/#<clickup-id>/<title>`.
- **PRs** — `gh pr create --assignee @me`, ready-for-review by default.
- **Style** — direct, terse; match surrounding Go; simple over clever.

This file documents the *codebase* (architecture, design decisions, roadmap), not collaboration rules.

## Architecture

```
cmd/work/main.go           Entry point, CLI arg routing
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
templates/
  base.md.tmpl              Base template (not yet wired — prompts generated in helpers.go)
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
  name: Anton Sandbye
  email: anton@airwallet.net
  clickup_uid: "55685515"
base_branches: [master, user_test]
```

Per-project config at `<repo-root>/.work.yaml` (overrides global):
```yaml
clickup:
  lists:
    operations: "901513634165"
    features: "901507776242"
verify:
  - pnpm check-types
  - pnpm check-circular
setup: pnpm cleanup
base_branches: [master, user_test]
```

## Key Design Decisions

1. **Composable config** — global defaults + per-project overrides. No hardcoded repo-specific stuff in the binary.
2. **Remote branch check in clean** — `git fetch --prune` then checks if `origin/<branch>` exists. Shows "gone" / "active" per worktree.
3. **Age from last commit** — not filesystem mtime. More meaningful for staleness.
4. **Direct create shortcut** — `work "hint"` skips TUI entirely for fast worktree creation (picks first base branch).
5. **ExecProcess for Claude** — Bubble Tea hands off terminal control to Claude via `tea.ExecProcess`. Alt screen exits cleanly.
6. **Prompt generation in code** — `GeneratePrompt()` in helpers.go builds the `.worktree.md` from config. Templates exist in `templates/` but aren't wired to Go's `text/template` yet.

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

### High Priority
- [ ] **Wire Go templates** — use `text/template` with `templates/*.md.tmpl` instead of string building in `GeneratePrompt()`. Support user template overrides from `~/.config/work/templates/`.
- [ ] **Port full prompt content** — current `GeneratePrompt()` is a skeleton. The original templates at `~/.worktree-template.md` and `~/.worktree-review-template.md` have rich ClickUp workflows, task type behaviors, PR review checklists, verification steps. Need to port all of that into the composable template system.
- [ ] **Per-project `.work.yaml`** — add to Airwallet repo with ClickUp lists, verify commands, setup command.
- [ ] **`--cd` subcommand** — jump into worktree shell (was in bash script, not yet ported).
- [ ] **Review session type** — `create_review_and_launch` equivalent. The TUI flow exists but review-specific prompt template needs work.

### Medium Priority
- [ ] **Template layers** — base (identity, git rules) + project (ClickUp, verify) + session type (task startup vs review checklist) + runtime vars (hint, branch, base). Compose at launch.
- [ ] **Homebrew tap** — already have `homebrew-tap` repo. Add formula for `work`.
- [ ] **`--watch` integration** — spawn PR watch subagent from TUI.
- [ ] **Tab-based navigation** — Active | New | Clean tabs instead of nested views.

### Low Priority
- [ ] **Zsh completions** — generate completions for flags.
- [ ] **`work --update`** — self-update from GitHub releases.
- [ ] **Session history** — track which worktrees were created, when, what task, outcome.
- [ ] Remove old bash script at `~/.local/bin/work` once Go version is stable.

## Build & Install

```bash
cd ~/Documents/GitHub/work
go build ./cmd/work/       # build
go install ./cmd/work/     # install to ~/go/bin/work
```

Binary goes to `~/go/bin/work` which takes precedence over `~/.local/bin/work` in PATH.
