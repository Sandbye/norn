# norn

**many threads, one tree** · git worktrees + coding-agent sessions, woven together.

![norn demo](assets/demo.gif)

norn runs many coding agents at once and tells you which one needs you.

Each task gets its own git **worktree** (an isolated checkout) with its own agent session. The dashboard reads each session's live state, so a thread whose turn has ended and is waiting on you shows up at a glance, instead of being found by cycling through terminals. When one flips to waiting, norn pings you. norn creates the worktrees, tracks them, and cleans them up, so you jump between tasks without stashing or branch-juggling.

Named for the Norns, who weave the threads of fate at the roots of the world tree. Your worktrees are the threads, git is the tree.

## Install

```sh
brew tap sandbye/norn
brew install norn
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

- **Threads** — live dashboard of every worktree session across your repos: agent state (working / waiting / idle), branch, PR state, age, and the next action from the worktree's own notes. `⏎` cd's in, `o` opens the agent, `/` filters, `m` jumps to the main checkout.
- **New** — create a worktree from a hint, with a Conventional Branch name. `T` seeds it from a real tracker task, `M` picks the model.
- **Clean** — auto-selects worktrees whose work is merged or whose remote branch is gone, so pruning is one keystroke.
- **Settings** — edit config in place (agent, template, theme, toggles), with a global/project layer switch.

```sh
norn                      # the TUI
norn create "add caching" # new worktree + branch, launch a session
norn create --branch foo  # worktree on an existing branch (that ref, no new branch; --checkout is an alias)
norn review 42            # check out PR #42 into a worktree, agent reviews it
norn diff                 # review uncommitted changes (or: --base, <pr#>)
norn --help               # everything
```

**`norn review <pr#>`** checks the PR's branch out into a worktree and launches the agent on the real code, so it can read, run, and comment on the actual change (fork PRs included). For a quick read without a worktree, the **diff** viewer does uncommitted changes, branch-vs-base, or any open PR, with split view and syntax highlighting; `norn diff <pr#> --since-review` overlays your own review comments next to the current code.

### Reviewing the agent locally

You don't need a PR (or a public repo) to review the agent's work. In any `norn diff`, `c` comments the focused line, `v` first for a range, `C` for the whole file. Each comment starts with a [conventional-comment](https://conventionalcomments.org) label picked with one key — `i` issue, `s` suggestion, `n` nitpick, `q` question, `t` todo, `p` praise, `h` thought, `o` chore — and `b` marks it blocking. `tab` re-picks the label while you type, `x` deletes, `R` finishes the review with a summary.

On a local diff, `R` writes `.norn/review.md`: comments grouped by file, anchored `path:line`, blocking ones counted at the top. Then `⏎` resumes the agent in that worktree pointed at the file, so one pass replaces a dozen round-trips. On a PR diff the same keys post a real GitHub review instead.

Add `.norn/` to the repo's `.gitignore` — the review is yours, not part of the change.

### Headless: `norn brief`

Other tools shouldn't have to reimplement branch naming or config resolution to start a task the way norn would. `norn brief` prints what norn knows, as JSON, and creates nothing — no worktree, no branch, no push, no agent:

```sh
norn brief --repo ~/mirrors/skuld.git --issue 7
norn brief --repo . --hint "fix payout rounding" --type fix
```

It runs from anywhere, including outside a git repo, and `--repo` accepts a checkout, a worktree, or a bare mirror — so a bot holding only a mirror gets the same branch name, brief, and project policy (`verify`, `forbid`, `base_branches`, …) a human would. `config.sources` lists the config files that were actually read, so "no project config" is distinguishable from "project config with no verify commands". Exit is non-zero only on a real error (unresolvable repo, unknown issue).

## Configuration

Zero config works. Global defaults live in `~/.config/norn/config.yaml`. `norn init` scaffolds a personal config for the repo you're in at `~/.config/norn/projects/<repo>.yaml`; a `.norn.yaml` committed at the repo root is the shared layer your whole team gets. Merge order is global, then `.norn.yaml`, then personal. The **Settings** tab writes to the YAML surgically, so your comments and hand-added keys survive.

Common knobs: `worktree_dir`, `base_branches`, `pr_base`, `ai_naming`. Run `norn --project-config` to print the resolved config, `norn doctor` to see what's wired up.

Upgrading from a version that used the `work` name? Nothing to do. norn reads `~/.config/work`, `~/.cache/work`, `~/.local/state/work` and `.work.yaml` wherever it finds no `norn`-named equivalent, and `norn doctor` prints the `mv` that retires each one. It never moves your files for you.

### Agent

norn launches a coding agent per worktree. Defaults to [Claude Code](https://claude.com/claude-code); any CLI agent works:

```yaml
agent:
  command: claude   # claude (default) | opencode | aider | …
  model: sonnet     # optional default model
```

With `claude`, norn injects the task brief via `--append-system-prompt` and resumes with `-c`. Any other agent is launched in the worktree directory, where the generated `.worktree.md` carries the brief. Thread summaries and AI branch naming are Claude-only and simply don't run otherwise.

### Notifications

A thread that finishes its turn is waiting on you, and with a dozen running you want to be told rather than to watch. On each dashboard refresh norn pings when a thread flips into `waiting`: a terminal bell, plus a desktop notification (`osascript` on macOS, `notify-send` on Linux) where one is available. Once per flip, never repeated while the state holds, and never for a thread that was already waiting when norn started.

```yaml
notify: true   # default; set false for silence
```

Agent state is read from Claude Code's local session transcripts, so this is Claude-only and simply doesn't run for other agents.

### Templates

Each worktree gets a `.worktree.md` brief from a template. norn ships `task` and `review`; drop your own in `~/.config/norn/templates/<name>.md.tmpl` to shadow a built-in.

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

## Support

norn is free and MIT-licensed. If it saves you time, you can [sponsor its development](https://github.com/sponsors/Sandbye). Entirely optional; stars and issues help just as much.

## License

[MIT](LICENSE) © Anton Sandbye
