# norn

**many threads, one tree** · git worktrees + Claude Code sessions, woven together.

`norn` is a terminal UI for running many pieces of work in parallel. Each task gets its own git **worktree** (an isolated checkout), and `norn` spins up, tracks, and cleans them up so you can jump between tasks without stashing, branch-juggling, or losing context. It pairs naturally with [Claude Code](https://claude.com/claude-code): launch an agent per worktree, then watch them all from one dashboard.

Named for the Norns, who weave the threads of fate at the roots of the world tree. Your worktrees are the threads; git is the tree.

## What it does

- **Dashboard** of every active worktree session across your repos: branch, PR state, age, at a glance.
- **Create** a worktree from a hint (`norn "fix the payout bug"`) with a sensible Conventional Branch name. With Claude available, it can name the branch from a task title too.
- **Diff** viewer: uncommitted changes, branch-vs-base, or any open PR, with split view, syntax highlighting, and a `--since-review` mode that overlays your own review comments next to the current code (fixes GitHub's "outdated comment" blind spot).
- **Clean**: auto-selects worktrees whose work is merged or whose remote branch is gone, so pruning is one keystroke.
- **cd** straight into any worktree from the dashboard.

## Install

```sh
go install github.com/sandbye/norn/cmd/norn@latest
```

Requires **Go 1.25+** to build. Then make sure `~/go/bin` is on your `PATH`.

### cd integration (optional)

For `norn` to drop your shell *into* a chosen worktree, add a small wrapper to your `~/.zshrc` / `~/.bashrc`:

```sh
norn() {
  command norn "$@"
  local t="$HOME/.cache/work/cd-target-$$"
  [ -f "$t" ] && { cd "$(cat "$t")"; rm -f "$t"; }
}
```

Without it, `norn` still works; it just can't change your current shell's directory.

## Quickstart

```sh
norn                     # dashboard of all worktree sessions
norn "add rate limiting" # create a worktree + branch, launch a session
norn diff                # review your uncommitted changes
norn --clean             # prune finished worktrees
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

`norn init` scaffolds a per-project config in the current repo (detects your stack, base branches, verify commands). Global defaults live in `~/.config/work/config.yaml`. Everything has sane defaults, so you can also just run `norn` with no config at all.

Key knobs: `worktree_dir`, `base_branches`, `pr_base`, `ai_naming` (set `false` to disable AI branch naming). See `norn --project-config` to print the resolved config.

### Agent

norn launches a coding agent per worktree. It defaults to [Claude Code](https://claude.com/claude-code), but any CLI agent works:

```yaml
agent:
  command: opencode   # claude (default) | opencode | aider | …
  args: []            # extra args for non-claude agents
```

With `claude`, norn injects the task brief via `--append-system-prompt` and resumes with `-c`. With any other agent, norn just launches it in the worktree directory, where the generated `.worktree.md` carries the brief (point your agent's `AGENTS.md`/rules at it if it doesn't read it automatically). The headless extras (thread summaries, AI branch naming) are Claude-specific and simply don't run for other agents.

### Footers & MCPs

These belong to your agent, not norn. Clickable footer badges (e.g. linking a ClickUp task) are a Claude Code `settings.json` feature (`footerLinksRegexes`), and MCP servers are configured in your agent's own config. norn launches the agent inside the worktree; it stays out of the agent's configuration.

## Theme

Ships with the [Nord](https://www.nordtheme.com/) palette (arctic frost + aurora), fitting the name.

## License

[MIT](LICENSE) © Anton Sandbye
