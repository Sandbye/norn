# norn

**many threads, one tree** · git worktrees + coding-agent sessions, woven together.

![norn demo](assets/demo.gif)

norn is a terminal UI for running many pieces of work in parallel. Each task gets its own git **worktree** (an isolated checkout); norn creates, tracks, and cleans them up, and launches a coding agent per worktree so you can jump between tasks without stashing or branch-juggling.

Named for the Norns, who weave the threads of fate at the roots of the world tree. Your worktrees are the threads, git is the tree.

## Install

```sh
brew install sandbye/norn/norn
```

Or with Go (needs 1.25+, and `~/go/bin` on your `PATH`):

```sh
go install github.com/sandbye/norn/cmd/norn@latest
```

### cd integration (optional)

A child process can't change its parent shell's directory, so for `⏎` to drop your shell *into* a worktree, add the shell wrapper to your rc file:

```sh
eval "$(norn shell-init zsh)"   # zsh · bash · fish
```

The wrapper lives in the binary, so it never drifts. Without it norn still works, it just can't move your current shell.

## Usage

norn is one tabbed TUI. `Tab` / `1`-`4` switch tabs, `?` shows keys, `esc` backs out, `q` quits.

- **Threads** — live dashboard of every worktree session across your repos (branch, PR state, age). `⏎` cd's in, `o` opens the agent, `/` filters, `m` jumps to the main checkout.
- **New** — create a worktree from a hint, with a Conventional Branch name. `T` seeds it from a real tracker task, `M` picks the model.
- **Clean** — auto-selects worktrees whose work is merged or whose remote branch is gone, so pruning is one keystroke.
- **Settings** — edit config in place (agent, template, theme, toggles), with a global/project layer switch.

```sh
norn                      # the TUI
norn create "add caching" # new worktree + branch, launch a session
norn diff                 # review uncommitted changes (or: --base, <pr#>)
norn --help               # everything
```

The **diff** viewer does uncommitted changes, branch-vs-base, or any open PR, with split view and syntax highlighting. `norn diff <pr#> --since-review` overlays your own review comments next to the current code.

## Configuration

Zero config works. `norn init` scaffolds a per-project config in the current repo; global defaults live in `~/.config/work/config.yaml`. The **Settings** tab writes to the YAML surgically, so your comments and hand-added keys survive.

Common knobs: `worktree_dir`, `base_branches`, `pr_base`, `ai_naming`. Run `norn --project-config` to print the resolved config, `norn doctor` to see what's wired up.

### Agent

norn launches a coding agent per worktree. Defaults to [Claude Code](https://claude.com/claude-code); any CLI agent works:

```yaml
agent:
  command: claude   # claude (default) | opencode | aider | …
  model: sonnet     # optional default model
```

With `claude`, norn injects the task brief via `--append-system-prompt` and resumes with `-c`. Any other agent is launched in the worktree directory, where the generated `.worktree.md` carries the brief. Thread summaries and AI branch naming are Claude-only and simply don't run otherwise.

### Templates

Each worktree gets a `.worktree.md` brief from a template. norn ships `task` and `review`; drop your own in `~/.config/work/templates/<name>.md.tmpl` to shadow a built-in.

```sh
norn --templates                    # list templates + the data they can use
norn create "hint" --template spike # use one for a single worktree
norn template edit task             # customize a template in $EDITOR
```

### Tasks

The **New** tab can seed a worktree from a real tracker task (`T`): the branch name and hint get filled for you.

```yaml
tasks:
  provider: github   # github | clickup | none
```

- **github** — open issues via the `gh` CLI (your existing auth, no token).
- **clickup** — tasks assigned to you; set `CLICKUP_TOKEN` (or `clickup.token`). `norn auth` walks you through scoping it.

norn only *seeds* the worktree; the agent's own MCP does the deep work. norn is not an MCP client.

## Themes

```yaml
theme: nord   # nord (default) | frog
```

nord is arctic frost + aurora ([Nord](https://www.nordtheme.com/)); frog is mossy forest greens with a 🐸 in the dashboard. Adding a palette is a few lines in `internal/tui/styles.go`, PRs welcome.

## Requirements

`git` is required. `gh` (GitHub CLI) unlocks the PR features; `claude` (or another agent) launches the sessions. Both are optional, the rest degrades gracefully.

## Contributing

```sh
git clone https://github.com/sandbye/norn && cd norn
go build ./... && go test ./...
```

Conventional Commits, focused changes. Good first PRs: themes, templates, agent presets.

## License

[MIT](LICENSE) © Anton Sandbye
