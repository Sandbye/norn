# Strand panes: norn as the seat you never leave

Design, 2026-09-17. Extends epic #67 (a task split across several agents) past
#71 (headless roles merge into trunk when they exit).

## The problem

A split task today runs its roles with `claude -p`. They are invisible while
they work, they cannot be interrupted, and answering one means opening another
terminal. Every one of those is a context switch, which is the cost norn exists
to remove. A role you have to leave norn to steer is a role norn only files.

## Vocabulary

- **Thread** is the task: one piece of work, one trunk branch, one PR.
- **Strand** is one agent's share of it: its own worktree, its own branch, its
  own live agent.

Used in the UI and docs from here. Renaming the code's `state.Session` is a
separate refactor and not part of this.

## What was decided

1. **Cockpit shape.** The rail of strands stays on the left; the rest of the
   screen is the selected strand, live. One key switches, and the strand you
   leave keeps running.
2. **Strands are live.** Every strand is a real interactive agent from the
   moment it is spawned, whether or not you are watching. Not `-p`: you cannot
   interrupt a one-shot, and interruption is the point.
3. **Roles are presets, not a fixed shape.** `.norn.yaml` keeps naming the
   roles you use often, and a strand can be added to a live thread at any time:
   name it, pick the agent, it branches off the trunk and joins the rail.
4. **Attention is the terminal bell.** BEL (0x07) from a strand's output marks
   it as wanting you. The existing claude transcript probe refines that into
   what kind of waiting it is where it can. An agent that never rings shows as
   working until it exits.
5. **Landing is explicit.** A strand that exits sits in the rail as `ready`
   with its commit count, and one key weaves it into the trunk. Exit no longer
   merges by itself, because with a live pane exit also means "I quit to look
   at something", and a merge commit is not something you undo casually.
6. **Strands outlive norn.** Each runs inside its own detached tmux session, so
   quitting the TUI, closing the lid or dropping an SSH connection does not
   kill work in progress.

## Architecture

Three new packages; everything else is reuse.

- `internal/pty` — attaches to a strand's tmux session inside a pseudo-terminal
  (`creack/pty`), feeds the bytes to a VT emulator (`charmbracelet/x/vt`), and
  exposes a rendered screen, a write path, and a BEL signal. It knows nothing
  about agents, roles or git.
- `internal/strand` — one strand's runtime: its tmux session, worktree, role,
  agent, state and exit. "Exited with 3 commits and a clean tree" becomes a
  fact here. `internal/taskrun` folds into this, since waiting on exits is now
  a strand's own business rather than a supervisor's.
- `internal/tui/pane.go` — the view: rail left, selected strand filling the
  rest, keystrokes forwarded when the pane has focus.

Reused unchanged: `internal/git/merge.go` for landing, `state.Task.Trunk` as
the merge target, the conflict-blocks-the-task rule, the per-strand run logs,
and `internal/config`'s roles.

## Data flow

**Your keys.** Bubble Tea `KeyMsg` → encoded as the bytes a terminal sends →
written to the PTY → the agent reacts as if you were sitting in its terminal.
No interpretation and no allowlist, which is why slash commands, `ctrl-c`,
permission prompts and whatever ships next all work without norn knowing about
them. norn keeps one key for itself: the one that returns focus to the rail.

**The agent's bytes.** tmux → PTY → VT emulator → screen buffer → rendered on
each frame. BEL is intercepted on the way and marks the strand. Exit is the
terminal event: norn records the commit count and whether the tree is clean,
and the rail shows `ready`.

**Landing.** The land key runs the existing `git.MergeNoFF` into
`state.Task.Trunk`, read from the task record. A conflict leaves the trunk
worktree exactly as git left it and marks the task blocked, unchanged from #71.

## State

`state.Session.Run` carries the strand lifecycle: `running` (live), `ready`
(exited, has commits, clean), `landed`, `failed`. `RunPID` still identifies the
live agent, and a dead pid with no norn around is how a restart learns a strand
finished while it was gone. `Task.Blocked` is unchanged.

## Testing

- `internal/pty` against `sh`: print text and assert the rendered screen, print
  `\a` and assert the attention signal, exit non-zero and assert the code.
- `internal/strand` reuses the git fixtures in `internal/taskrun`'s tests,
  which already build a trunk plus a role worktree.
- No golden-file tests of the pane. The screen buffer is the testable surface;
  the frame around it is not.

## Risks

- `charmbracelet/x/vt` is experimental and its API can move. Contained to
  `internal/pty`, which is one file with tests against `sh`.
- norn is on Bubble Tea v1.3.10 while the one public precedent for this wiring
  ([bubble-ssh](https://github.com/muhamm-ad/bubble-ssh)) is v2, so key
  encoding is ours to write.
- Each pane needs its size propagated, or agents wrap their output wrongly.
- N emulators is N buffers. Only the visible strand renders per frame.
- N live agents spend tokens whether or not you are watching. That is the cost
  of decision 2, accepted knowingly.
- tmux becomes a hard dependency.

## Alternatives rejected

- **`tmux capture-pane` + `send-keys` instead of attaching.** No emulator at
  all, and scrollback for free. Rejected because it polls: your own typing
  echoes back a frame late and every keystroke is a subprocess. The lag lands
  exactly where it is felt.
- **tmux control mode (`-CC`).** Streams rather than polls, but `%output`
  blocks carry raw terminal output, so a client still has to emulate them (this
  is what iTerm2 does). Same emulator cost as attaching, plus a protocol.
- **The agents' own daemons.** Claude Code's session daemon (`claude attach`,
  already used by norn in `internal/tui/helpers.go`) and `codex app-server`
  both give durability with no multiplexer. Rejected because durability would
  then be a per-agent capability, and every new agent would be supported or not
  — the adapter tax that #71's exit-code contract and decision 4's terminal
  bell both exist to avoid. Worth revisiting if tmux proves a burden.
- **dtach / abduco.** Smaller than tmux and enough for detaching, but no
  scrollback and rarely installed.
- **zellij.** Nicer protocol, rarely installed, embedding API not stable.
- **A norn daemon owning the PTYs.** No external dependency and full control,
  but the largest piece of work here and a second process to debug.

## Done when

A thread with three strands on mixed agents runs with all three live; you enter
any of them, interrupt it, answer it and leave it running; quitting norn leaves
every strand working; and a strand you land reaches the trunk without a git
command being typed.

## Ordered strands (`after:`)

A role may declare that it starts only once another has landed:

```yaml
roles:
  tests:  { agent: claude }
  logic:  { agent: claude, after: tests }
  integration: { agent: claude, integrates: true }
```

Optional. A role with no `after:` spawns at create, as every strand does today.

This exists because TDD is a sequence, not a division of labour: tests, a human
reading the failing tests, implementation to green, then refactoring. Some repos
enforce the machine-checkable half in CI, walking a pull request's commits in
order and requiring the first change under `src/` to come after one under
`test/`. Parallel strands make that ordering a coin flip, because each commits
on its own branch whenever it gets there.

With `after:`, the order is real rather than reconstructed. The dependent strand
is spawned when its dependency lands, and landing is already a keypress, so the
human review sits exactly where TDD puts it: you read the red tests, land them,
and that landing starts the implementation from a trunk that contains them. The
PR range then has test commits before src commits as a fact of the history, not
as something norn arranged afterwards.

## Settled after the first review

- **The trunk is a strand**, with its own live agent, marked integrating. It is
  where a conflict gets resolved and where the PR is opened, and both have to
  be possible without leaving the seat.
- **An agent that never rings shows `working`, with the age of its last
  output.** Honest about what norn knows, and the age is what tells you
  something has stalled.
- **`norn run` survives**, and now means "spawn every declared role's strand,
  live". The orchestrator epic #67 ends in drives the same verbs a person
  types, so the verb has to keep existing.
