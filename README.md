# norn

**many threads, one tree** · git worktrees + Claude Code sessions, woven together.

`norn` is a terminal UI for running many pieces of work in parallel. Each task gets its own git **worktree** (an isolated checkout), and `norn` spins up, tracks, and cleans them up so you can jump between tasks without stashing, branch-juggling, or losing context. It pairs naturally with [Claude Code](https://claude.com/claude-code): launch an agent per worktree, then watch them all from one dashboard.

Named for the Norns, who weave the threads of fate at the roots of the world tree. Your worktrees are the threads; git is the tree.

## What it does

`norn` is a single tabbed TUI. `Tab`/`shift+Tab` or `1`–`4` switch tabs, `esc` backs out, `?` toggles the key list, `q` quits.

- **Threads** — a live dashboard of every worktree session across your repos: branch, PR state, age, at a glance. `⏎` cd's into one, `o` opens the agent, `/` fuzzy-filters.
- **New** — create a worktree from a hint with a sensible Conventional Branch name. With Claude available, it can name the branch from a task title too.
- **Clean** — auto-selects worktrees whose work is merged or whose remote branch is gone, so pruning is one keystroke.
- **Settings** — edit config (agent, template, toggles) in place; free-form fields open in `$EDITOR`.

Plus a **Diff** viewer (`norn diff`): uncommitted changes, branch-vs-base, or any open PR, with split view, syntax highlighting, and a `--since-review` mode that overlays your own review comments next to the current code (fixes GitHub's "outdated comment" blind spot).

## Install

```sh
go install github.com/sandbye/norn/cmd/norn@latest
```

Requires **Go 1.25+** to build. Then make sure `~/go/bin` is on your `PATH`.

### cd integration (optional)

A child process can't change its parent shell's directory, so for `norn` to drop your shell *into* a chosen worktree, add its shell wrapper to your rc file:

```sh
# ~/.zshrc or ~/.bashrc
eval "$(norn shell-init zsh)"      # or: bash · fish
```

The wrapper lives in the binary, so it never drifts out of sync. Without it, `norn` still works; it just can't change your current shell's directory.

## Quickstart

```sh
norn                     # tabbed TUI (Threads · New · Clean · Settings)
norn "add rate limiting" # create a worktree + branch, launch a session
norn diff                # review your uncommitted changes
norn --clean             # open on the Clean tab
norn settings            # open on the Settings tab
norn --help              # everything
```

## Requirements

| Tool | Needed for |
|------|------------|
| `git` | everything (required) |
| `gh` (GitHub CLI) | PR features: `norn diff <pr#>`, `--since-review` |
| `claude` (Claude Code) | default agent; thread summaries + AI branch naming (Claude-only) |
| another agent | set `agent.command` (e.g. `opencode`) to launch it instead of `claude` |

`gh` and `claude` are optional. Without them, the related features degrade gracefully; the rest works. Run `norn doctor` to see what's wired up.

## Configuration

`norn init` scaffolds a per-project config in the current repo (detects your stack, base branches, verify commands). Global defaults live in `~/.config/norn/config.yaml`. Everything has sane defaults, so you can also just run `norn` with no config at all.

Key knobs: `worktree_dir`, `base_branches`, `pr_base`, `ai_naming` (set `false` to disable AI branch naming). See `norn --project-config` to print the resolved config.

Edit config without leaving your hand comments behind: the **Settings** tab (or `norn settings`) is a small TUI for the common knobs (agent, template, theme, toggles, paths) with a global/project layer switch. It writes surgically to the YAML, so comments and unmodeled keys survive. Free-form fields (verify commands, forbid rules, task lists) open in `$EDITOR`.

### Agent

norn launches a coding agent per worktree. It defaults to [Claude Code](https://claude.com/claude-code), but any CLI agent works:

```yaml
agent:
  command: opencode   # claude (default) | opencode | aider | …
  args: []            # extra args for non-claude agents
```

With `claude`, norn injects the task brief via `--append-system-prompt` and resumes with `-c`. With any other agent, norn just launches it in the worktree directory, where the generated `.worktree.md` carries the brief (point your agent's `AGENTS.md`/rules at it if it doesn't read it automatically). The headless extras (thread summaries, AI branch naming) are Claude-specific and simply don't run for other agents.

### Templates

Each worktree gets a `.worktree.md` brief rendered from a template. norn ships `task` and `review` templates; drop your own in `~/.config/norn/templates/<name>.md.tmpl` (a file there shadows the built-in of the same name).

```sh
norn --templates                 # list available templates (built-in + user)
norn "hint" --template spike     # use a specific template for one worktree
```

Set a default for new tasks with `template: <name>` in config. Templates are Go `text/template`; the data available (user, ClickUp lists, verify commands, base branch, …) is documented in the built-in `task` template.

### Tasks

The **New** tab can seed a worktree from a real tracker task instead of a freeform hint. Press `T` to pull up a picker; selecting a task fills the branch name (`fix/#123/slug`) and hint for you.

```yaml
tasks:
  provider: github    # github | clickup | none
```

- **github** — open issues via the `gh` CLI (reuses your gh auth, no token, no config).
- **clickup** — the tasks assigned to *you* across your workspaces. Just set a token; no list IDs to configure:
  ```sh
  export CLICKUP_TOKEN=pk_...   # from ClickUp → Settings → Apps
  ```
  (or `clickup.token` in config).

norn only uses this to *seed* the worktree; the launched agent's own MCP does the deep work. norn is not an MCP client.

### Footers & MCPs

These belong to your agent, not norn. Clickable footer badges (e.g. linking a ClickUp task) are a Claude Code `settings.json` feature (`footerLinksRegexes`), and MCP servers are configured in your agent's own config. norn launches the agent inside the worktree; it stays out of the agent's configuration.

## Themes

Two built-in palettes, switchable from the **Settings** tab or in config:

```yaml
theme: nord   # nord (default) | frog
```

- **nord** — arctic frost + aurora ([Nord](https://www.nordtheme.com/)), fitting the name.
- **frog** — mossy forest greens, with a 🐸 in the dashboard.

Adding a palette is a few lines in `internal/tui/styles.go` — PRs for new themes welcome.

## Contributing

Issues and pull requests are welcome. To hack on it:

```sh
git clone https://github.com/sandbye/norn && cd norn
go build ./...   # build
go test ./...    # tests
go run ./cmd/norn --help
```

Conventional Commits for messages; keep changes focused. Good first contributions: new themes, prompt templates, and agent presets.

## License

[MIT](LICENSE) © Anton Sandbye
