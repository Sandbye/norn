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
norn create "x" --roles logic,assets # one trunk + one worktree per role (see Roles)
norn run                  # run this task's headless roles, merge each into the trunk
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

### Roles

One task can be split across several agents, and the parts have names you already think in. `roles:` declares them and says which agent serves each:

```yaml
roles:
  logic:
    agent: claude
  assets:
    agent: codex
    model: gpt-5
  integration:
    agent: claude
    integrates: true
```

Each role takes the same keys as `agent:` (`command`, `args`, `model`), with `agent:` as shorthand for `command:`; a role spelling both is rejected rather than one silently winning. Exactly one role sets `integrates: true`: it is the one that merges the others' work. A config with none, or with two, fails to load and the error names the roles.

Roles layer per field like every other key, so a personal `~/.config/norn/projects/<repo>.yaml` can point one role at a different agent without restating the rest. A repo that declares no roles is unaffected: `agent:` stays the only thing deciding what launches.

Declaring roles does not split anything. A create asks for the split:

```bash
norn create "multi model" --roles logic,assets
```

That cuts one trunk branch off the base and one branch per role off the trunk, each in its own worktree:

```
main
 └─ feature/multi-model/CU-123/trunk    integrating role, the only branch that opens a PR
     ├─ feature/multi-model/CU-123/logic
     └─ feature/multi-model/CU-123/assets
```

The trunk takes a leaf of its own rather than being `feature/multi-model/CU-123`: git stores a ref as a file, so a branch of that name is exactly what would stop the role branches under it from existing.

`norn run` drives the split to done:

```bash
norn run          # from any of the task's worktrees
norn run <task-id>
```

Every non-integrating role runs headless in its own worktree (`claude -p` for claude, `codex exec --json` for codex, on the normal login in both cases), and process exit is the done signal. A role that exits 0 with commits is merged into the trunk with `--no-ff`; one that exits non-zero shows as failed with the trunk untouched; a merge that conflicts leaves the conflict in the trunk worktree for you and marks the task blocked. norn never opens or merges the PR: the integrating role stays interactive and opens the single PR from the trunk once the roles have landed.

A role's `args:` reaches the knobs norn has no opinion on, whichever agent serves it, `-c model_reasoning_effort=low` on a Codex role above all, since reasoning effort is the largest lever on what an unattended role costs. Args that repeat a flag norn sets itself are rejected when the config loads, so a role cannot quietly widen the sandbox or break the event stream the run is read from.

Each role's JSONL output goes to `~/.local/state/norn/runs/<task-id>/<role>.log`, since nobody is watching the run. Run state is written as it changes, so a `norn run` you kill can be re-run: a role that finished unwatched merges on the next run instead of starting over.

The integrating role is always included, so `--roles logic` still gives you two worktrees. Picking nothing, or picking only the integrating role, is one plain worktree on the usual branch name. Each worktree's `.worktree.md` names the role that owns it, the trunk it merges into, and the roles running in parallel; every row shares one task id, and a create that fails part-way removes the worktrees and branches it had already made. `norn create` with no hint offers the same picker in the New tab.

### Notifications

A thread that finishes its turn is waiting on you, and with a dozen running you want to be told rather than to watch. On each dashboard refresh norn pings when a thread flips into `waiting`: a terminal bell, plus a desktop notification (`osascript` on macOS, `notify-send` on Linux) where one is available. Once per flip, never repeated while the state holds, and never for a thread that was already waiting when norn started.

```yaml
notify: true   # default; set false for silence
```

Agent state is read from Claude Code's local session transcripts, so this is Claude-only and simply doesn't run for other agents.

### Answering a thread without entering it

Most waiting threads need one word: yes, the second option, go ahead. Paying a full context switch for one word is the cost this removes. Press `i` on a waiting thread, type the answer, press `⏎`. norn sends it to that thread's existing session with `claude -p --continue`, so nothing takes over your terminal and the thread flips back to working on the next refresh.

Only a thread that is actually waiting can be answered. Claude Code resumes a session that has finished but not one that is still running, so answering a working thread would open a new session instead of continuing the one on screen.

By default the reply runs under Claude Code's `-p` permissions, which is Manual: a tool that would normally ask you is denied, because nobody is watching to approve it. The agent is told so and reports back. That is fine for answering a question, and not enough for a reply that should go on to change files. To allow that:

```yaml
reply_permission_mode: acceptEdits   # or auto; default is Manual
```

That grants an unattended run authority over your files, which is why it is off unless you ask for it.

### Re-entry: status bar and resume

Coming back to a thread is the expensive part, and it is expensive twice: you rebuild "where was I", and then the agent rebuilds it too by reading files while you watch the tokens go. Two small pieces fix each half.

**Status bar.** `norn statusline` renders the thread's card for Claude Code's status line: branch, context use, the `next` action from `.state.md`, and anything blocking it. It runs locally and the model never sees it, so it costs no tokens.

```json
{
  "statusLine": { "type": "command", "command": "norn statusline" }
}
```

Note that configuring any custom status line makes Claude Code drop most of its footer keyboard hints, `esc to interrupt` among them.

**Resume.** Hand the same file to the model once, at session start, instead of letting it rediscover:

```json
{
  "hooks": {
    "SessionStart": [
      { "matcher": "resume",
        "hooks": [{ "type": "command", "command": "cat .state.md 2>/dev/null" }] }
    ]
  }
}
```

Hook stdout on `SessionStart` is added to the model's context, charged once for the session. `.state.md` is already the shape for this: goal, next action, blocker, and the decisions a later session would otherwise re-derive. norn's dashboard reads the same file, so the TUI, your status bar and the agent all resume from one source.

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

To try a change without replacing your installed norn, build it under another name. The shell wrapper is named after the binary, so the second build gets a working `⏎` of its own:

```sh
go build -o ~/go/bin/norn-dev ./cmd/norn
eval "$(norn-dev shell-init zsh)"
```

Both builds share `~/.config/norn` and the session store, so `norn-dev` shows the same threads.

Conventional Commits, focused changes. Good first PRs: themes, templates, agent presets.

## Support

norn is free and MIT-licensed. If it saves you time, you can [sponsor its development](https://github.com/sponsors/Sandbye). Entirely optional; stars and issues help just as much.

## License

[MIT](LICENSE) © Anton Sandbye
