# norn

**many threads, one tree** · git worktrees + coding-agent sessions, woven together.

![norn demo](assets/demo.gif)

norn is a terminal workspace for running several coding agents at once. Each task gets its own git worktree, its own branch and its own agent session; norn creates them, shows which one needs you, lets you step into any of them, review what they wrote, merge their work and open one pull request at the end.

It is a single TUI, plus a small CLI for the parts other tools need. It holds no credentials: it launches each agent's own binary, which authenticates itself.

Named for the Norns, who weave the threads of fate at the roots of the world tree. Your worktrees are the threads, git is the tree.

---

## Contents

- [Install](#install)
- [Quick start](#quick-start)
- [Concepts](#concepts)
- [The TUI](#the-tui)
- [Keys](#keys)
- [Splitting a task across agents](#splitting-a-task-across-agents)
- [Reviewing before anything reaches a remote](#reviewing-before-anything-reaches-a-remote)
- [Headless and scripting](#headless-and-scripting)
- [Configuration](#configuration)
- [Notifications](#notifications)
- [Re-entry: status bar and resume](#re-entry-status-bar-and-resume)
- [Templates](#templates)
- [Tracker tasks](#tracker-tasks)
- [Themes](#themes)
- [Requirements](#requirements)
- [Contributing](#contributing)

---

## Install

```sh
brew tap sandbye/norn
brew install norn
```

Or with Go (needs 1.25+, and `~/go/bin` on your `PATH`):

```sh
go install github.com/sandbye/norn/cmd/norn@latest
```

### Shell integration (optional)

A child process cannot change its parent shell's directory, so for `⏎` to drop your shell *into* a worktree, add the wrapper to your rc file:

```sh
eval "$(norn shell-init zsh)"   # zsh · bash · fish
```

The wrapper lives in the binary, so it never drifts. Without it norn still works, it just cannot move your current shell.

---

## Quick start

```sh
norn create "fix payout rounding"   # worktree + branch + agent session
norn                                # the TUI: every thread, with live state
norn diff                           # review what was written, before any commit
```

One task, one worktree, one agent. When you want more than one agent on the same task, see [Splitting a task across agents](#splitting-a-task-across-agents).

---

## Concepts

| Term | What it is |
|---|---|
| **thread** | One task. It may be a single worktree, or a trunk with several strands under it. |
| **strand** | One agent, in one worktree, on one branch, owning one part of a task. |
| **trunk** | The task's own branch. Strands merge into it, and it is the only branch that opens a pull request. |
| **role** | A named job a strand does (`plan`, `tests`, `logic`, `review`, `integration`), declared in config. |
| **shape** | A named, ordered subset of roles: what "a feature" or "a small fix" means in this repo. |
| **plan** | A fan-out a planning strand writes, one entry per strand it wants created. Nothing is created until you accept it. |

A task's branches look like this:

```
main
 └─ feature/payout-rounding/CU-123/trunk    the integrating strand; the only branch that opens a PR
     ├─ feature/payout-rounding/CU-123/tests
     └─ feature/payout-rounding/CU-123/logic
```

The trunk takes a leaf of its own rather than being `feature/payout-rounding/CU-123`: git stores a ref as a file, so a branch of that name is exactly what would stop the strand branches under it from existing.

---

## The TUI

`Tab` or `1`-`5` switch tabs, `?` shows keys, `esc` backs out, `q` quits.

- **Threads** — every worktree across your repos: agent state (working / waiting / idle), branch, pull-request state, age, and the `next` action from the worktree's own notes. This is where you spend your time.
- **New** — create a worktree from a hint, with a Conventional Branch name. `T` seeds it from a tracker task, `M` picks the model.
- **Clean** — preselects worktrees whose work is merged or whose remote branch is gone, so pruning is one keystroke.
- **Settings** — edit config in place, with a scope switch between global, shared-repo and personal layers, showing which layer owns each value.
- **Tasks** — the tracker list, when one is configured.

### Strand panes

`→` on a strand opens that agent's terminal inside norn. It is the real program, in a real pseudo-terminal: what you see is what you would see in its own window, and what you type goes to it.

Each strand runs in a [tmux](https://github.com/tmux/tmux) session that norn owns (`tmux -L norn`), so the agent survives norn quitting, a lost SSH connection, or a crash; reopening the pane reattaches to the same session rather than starting a new one.

Because the agent has the keyboard, norn's own keys sit behind a leader, the way tmux does it: press `ctrl+a`, then one key. Pressing the leader twice sends a literal one through, so nothing the agent binds becomes unreachable. Change it with `pane_leader:` in config.

### Task board

`b` opens the board for the task under the cursor: every strand, what state it is in, how many commits it has that the trunk does not, what it says it is doing next, and one line answering whether the task can ship. A split task's truth is otherwise spread across several transcripts.

### Plan reader

`S` on a planning strand opens the plan it wrote: one line per proposed strand with its `after:` and `expect:`, `⏎` to read one brief, `e` to open the file in `$EDITOR`. `L` accepts it, which is what creates the strands.

---

## Keys

**Threads**

| Key | Does |
|---|---|
| `⏎` | cd into the worktree |
| `o` | open the agent |
| `→` | enter a strand's live pane |
| `f` | jump to any strand by name (telescope-style picker) |
| `b` | task board |
| `S` | read a planning strand's plan |
| `L` | land a finished strand on the trunk |
| `R` | spawn this task's strands |
| `P` | approve the pull request the integrating strand is waiting to open |
| `i` | answer a waiting thread without entering it |
| `s` | summarize the branch (`esc` stops it) |
| `p` / `t` | open the pull request / the tracker task |
| `d` | clean this worktree |
| `/` `a` `r` | filter · all repos · refresh |

**Inside a strand pane** (after the `ctrl+a` leader)

| Key | Does |
|---|---|
| `←` | back to the rail |
| `↓` / `↑` | next / previous strand |
| `f` | strand picker |
| `b` | task board |
| `ctrl+a` | send a literal `ctrl+a` to the agent |

**Task board**

| Key | Does |
|---|---|
| `↓` / `↑` | move |
| `→` | enter that strand |
| `d` | review that strand's work, and hand the review back to it |
| `S` | read the plan |
| `L` | land it |

---

## Splitting a task across agents

Declare the roles once, in `.norn.yaml` (shared with your team) or `~/.config/norn/projects/<repo>.yaml` (yours):

```yaml
roles:
  plan:        { agent: claude, plans: true }
  tests:       { agent: claude, after: plan,  expect: red }
  logic:       { agent: claude, after: tests, expect: green }
  refactor:    { agent: claude, after: logic, expect: green }
  review:      { agent: claude, reviews: true }
  integration: { agent: claude, integrates: true }

shapes:
  feature: [plan, tests, logic, refactor, review, integration]
  small:   [logic, integration]
```

Each role takes the same keys as `agent:` (`command`, `args`, `model`), with `agent:` as shorthand for `command:`. Exactly one role sets `integrates: true`. Roles layer per field like every other key, so a personal config can point one role at a different agent without restating the rest.

Then create a split task:

```sh
norn create "multi model" --roles logic,assets   # explicit roles
norn create "add webhooks" --shape feature       # a named shape
```

### What each key in a role means

| Key | Meaning |
|---|---|
| `after: <role>` | This strand starts only when that role has landed, so its branch forks from a trunk that already holds that work. |
| `expect: red` | Refuses to land while the repo's `verify:` passes. This is the half of test-first a machine can check: a test that passes before the implementation exists proves nothing. |
| `expect: green` | Refuses to land while `verify:` fails. |
| `plans: true` | Writes the fan-out instead of code. At most one role. |
| `test_first: true` | On a planning role: every piece must be proposed as a pair, a failing test strand and an implementation strand that waits for it. |
| `reviews: true` | Starts when the last code strand lands, reads the combined change, and reports back. |
| `integrates: true` | Owns the trunk, and opens the single pull request, only after you press `P`. |

Every arrow in that sequence is a keypress of yours. norn never merges a strand, opens a pull request, or merges one, on its own.

### Planner-driven fan-out

A `plans: true` role does not write code. It reads the task, checks whether the work already exists (open and closed pull requests included), and writes a plan to norn's state directory, one entry per strand it wants:

```yaml
summary: split by service, one test strand per API surface
strands:
  - role: contract
    expect: green
    brief: |
      Owns the shared types. Declarations only, no behaviour.
  - role: enqueue-tests
    after: contract
    expect: red
    brief: |
      Pins the queue write with failing tests. Do not implement.
  - role: enqueue
    after: enqueue-tests
    expect: green
    brief: |
      Makes the enqueue-tests strand green.
```

Press `S` to read it, `L` to accept. Accepting creates the worktrees and starts the ones that are not waiting for another. The plan lives in `~/.local/state/norn/plans/<task-id>/strands.yaml`, never in your repo, so no linter or commit ever sees it.

### Running the strands

```sh
norn run             # from any of the task's worktrees
norn run <task-id>
```

`R` on the Threads tab does the same for the task under the cursor, detached, so the rail keeps rendering while the strands work. `R` is also the catch-up key: a strand whose predecessor landed while norn was not running starts on the next press.

Every non-integrating strand can run headless in its own worktree (`claude -p`, `codex exec --json`), and process exit is the done signal: exit 0 with commits merges into the trunk with `--no-ff`, a non-zero exit shows as failed with the trunk untouched, and a conflicting merge is left in the trunk worktree for you with the task marked blocked.

Each strand's output goes to `~/.local/state/norn/runs/<task-id>/<role>.log`. Run state is written as it changes, so a run you kill can be re-run: a strand that finished unwatched merges on the next run instead of starting over.

A role's `args:` reaches the knobs norn has no opinion on, `-c model_reasoning_effort=low` on a Codex role above all, since reasoning effort is the largest lever on what an unattended strand costs. Args that repeat a flag norn sets itself are rejected when the config loads, so a role cannot quietly widen the sandbox or break the event stream a run is read from.

---

## Reviewing before anything reaches a remote

You do not need a pull request, or even a commit, to review an agent's work.

`d` on the board opens the strand's diff against the trunk, **including uncommitted and untracked files**. An agent commits when it reaches a point it likes, which is after the moment worth saying "not that way".

In any diff: `c` comments the focused line, `v` first for a range, `C` for the whole file. Each comment starts with a [conventional-comment](https://conventionalcomments.org) label picked with one key, `i` issue, `s` suggestion, `n` nitpick, `q` question, `t` todo, `p` praise, `h` thought, `o` chore, and `b` marks it blocking. `tab` re-picks the label while you type, `x` deletes, `R` finishes with a summary.

On a local diff, `R` writes `.norn/review.md`, comments grouped by file and anchored `path:line`, and hands it straight back to that strand's session, so it fixes what you wrote without you leaving norn. On a pull-request diff the same keys post a real GitHub review.

Add `.norn/` to the repo's `.gitignore`: the review is yours, not part of the change.

```sh
norn diff                      # uncommitted changes
norn diff --base main          # branch vs base
norn diff 42                   # an open pull request
norn diff 42 --since-review    # your own comments overlaid on the current code
norn review 42                 # check the PR out into a worktree, agent reviews the real code
```

---

## Headless and scripting

### `norn brief`

Other tools should not have to reimplement branch naming or config resolution to start a task the way norn would. `norn brief` prints what norn knows, as JSON, and creates nothing: no worktree, no branch, no push, no agent.

```sh
norn brief --repo ~/mirrors/project.git --issue 7
norn brief --repo . --hint "fix payout rounding" --type fix
```

It runs from anywhere, including outside a git repo, and `--repo` accepts a checkout, a worktree, or a bare mirror, so a bot holding only a mirror gets the same branch name, brief and project policy (`verify`, `forbid`, `base_branches`, …) a human would. `config.sources` lists the files actually read, so "no project config" is distinguishable from "project config with no verify commands". Exit is non-zero only on a real error.

### Other commands

```sh
norn create "hint"                   # worktree + branch + session
norn create --branch foo             # worktree on an existing ref (--checkout is an alias)
norn create "x" --roles logic,assets # a trunk plus one worktree per role
norn run [task-id]                   # drive a split task's strands
norn tell <role> "message"           # send a message to a strand
norn statusline                      # render a thread's card for a status line
norn doctor                          # what is wired up, and what is not
norn --project-config                # the resolved config for this repo
norn --help                          # everything
```

---

## Configuration

Zero config works. Three layers merge in order:

1. `~/.config/norn/config.yaml` — global defaults.
2. `<repo>/.norn.yaml` — committed, what your whole team gets.
3. `~/.config/norn/projects/<repo>.yaml` — personal, yours only.

`norn init` scaffolds the personal one for the repo you are in. The Settings tab writes YAML surgically, so comments and hand-added keys survive, and shows which layer owns each value.

```yaml
worktree_dir: ~/worktrees
base_branches: [main, master]
branch_base: main
pane_leader: ctrl+a

agent:
  command: claude      # claude (default) | codex | aider | …
  model: sonnet

verify:                # what expect: red / green is measured with
  - go build ./...
  - go test ./...

setup: pnpm install    # run once in a new worktree
notify: true
theme: nord
```

With `claude`, norn injects the task brief via `--append-system-prompt` and resumes with `-c`. Any other agent is launched in the worktree directory, where the generated `.worktree.md` carries the brief. Thread summaries and AI branch naming are Claude-only and simply do not run otherwise.

Upgrading from a version that used the `work` name? Nothing to do: norn reads `~/.config/work`, `~/.cache/work`, `~/.local/state/work` and `.work.yaml` wherever it finds no `norn`-named equivalent, and `norn doctor` prints the `mv` that retires each one. It never moves your files for you.

---

## Notifications

A thread that finishes its turn is waiting on you, and with a dozen running you want to be told rather than to watch. norn pings when a thread flips into `waiting`: a terminal bell, plus a desktop notification (`osascript` on macOS, `notify-send` on Linux) where one is available. Once per flip, never repeated while the state holds, and never for a thread that was already waiting when norn started.

```yaml
notify: true   # default; set false for silence
```

Agent state is read from Claude Code's local session transcripts, so this is Claude-only.

### Answering without entering

Most waiting threads need one word. Press `i`, type the answer, press `⏎`: norn sends it to that thread's existing session, nothing takes over your terminal, and the thread flips back to working on the next refresh.

By default the reply runs under Claude Code's `-p` permissions, which is Manual: a tool that would normally ask you is denied, because nobody is watching to approve it. To allow a reply that goes on to change files:

```yaml
reply_permission_mode: acceptEdits   # or auto; default is Manual
```

That grants an unattended run authority over your files, which is why it is off unless you ask for it.

---

## Re-entry: status bar and resume

Coming back to a thread is expensive twice: you rebuild "where was I", and then the agent rebuilds it too by reading files while you watch the tokens go.

**Status bar.** `norn statusline` renders the thread's card for Claude Code's status line: branch, context use, the `next` action from `.state.md`, and anything blocking it. It runs locally and the model never sees it, so it costs no tokens.

```json
{
  "statusLine": { "type": "command", "command": "norn statusline" }
}
```

Note that configuring any custom status line makes Claude Code drop most of its footer keyboard hints, `esc to interrupt` among them.

**Resume.** Hand the same file to the model once, at session start:

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

Hook stdout on `SessionStart` is added to the model's context, charged once for the session. norn's dashboard reads the same file, so the TUI, your status bar and the agent all resume from one source.

---

## Templates

Each worktree gets a `.worktree.md` brief from a template. norn ships `task` and `review`; drop your own in `~/.config/norn/templates/<name>.md.tmpl` to shadow a built-in. A strand's role block (what it owns, what it must not touch, who merges) is appended to every brief whichever template you use.

```sh
norn --templates                    # list templates + the data they can use
norn create "hint" --template spike
norn template edit task
```

---

## Tracker tasks

The New tab can seed a worktree from a real tracker task (`T`): the branch name and hint get filled for you.

```yaml
tasks:
  provider: github   # github | clickup | none
```

- **github** — open issues via the `gh` CLI (your existing auth, no token).
- **clickup** — tasks assigned to you; set `CLICKUP_TOKEN` (or `clickup.token`). `norn auth` walks you through scoping it.

norn only *seeds* the worktree; the agent's own tooling does the deep work. norn is not an MCP client.

---

## Themes

```yaml
theme: nord   # nord (default) | frog
```

nord is arctic frost + aurora ([Nord](https://www.nordtheme.com/)); frog is mossy forest greens with a 🐸 in the dashboard. Adding a palette is a few lines in `internal/tui/styles.go`, pull requests welcome.

---

## Requirements

`git` is required. `tmux` is required for strand panes and for split tasks; everything else works without it. `gh` unlocks the pull-request features, and `claude` (or another agent CLI) launches the sessions. The rest degrades gracefully.

---

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

Conventional Commits, focused changes. Good first pull requests: themes, templates, agent presets.

## Support

norn is free and MIT-licensed. If it saves you time, you can [sponsor its development](https://github.com/sponsors/Sandbye). Entirely optional; stars and issues help just as much.

## License

[MIT](LICENSE) © Anton Sandbye
