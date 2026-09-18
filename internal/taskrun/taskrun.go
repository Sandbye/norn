// Package taskrun is the supervisor behind `norn run`: it drives every
// non-integrating role of a split task headless and merges each one into the
// trunk when its process exits.
//
// One process waiting on N children, merging on one consumer, so the merges
// serialize by construction rather than by a lock. The integrating role is
// never started here: that is the interactive thread, and it is what opens the
// single PR once the roles have landed.
package taskrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/sandbye/norn/internal/config"
	"github.com/sandbye/norn/internal/git"
	"github.com/sandbye/norn/internal/headless"
	"github.com/sandbye/norn/internal/paths"
	"github.com/sandbye/norn/internal/state"
)

// startRole is the seam the tests replace: everything else in this package is
// store bookkeeping and git, and neither is worth testing through a real agent.
var startRole = headless.Start

// Options is one `norn run`.
type Options struct {
	Cfg    config.Config
	TaskID string
	Out    io.Writer // progress, one line per transition
}

// ErrBlocked is a task nobody has unblocked yet: a merge conflict is still
// sitting in the trunk worktree. Starting more roles would pile work behind a
// merge that cannot finish, so the run refuses instead.
var ErrBlocked = errors.New("task is blocked")

// thread is one role the supervisor is responsible for this run.
type thread struct {
	sess  state.Session
	agent config.AgentConfig
}

// result is a role's process exit, the only done signal in this design.
type result struct {
	sess state.Session
	err  error // nil means exit 0
}

// Run starts every role that still needs running, waits for each to exit, and
// merges the ones that exit 0 with commits. It returns an error when any role
// failed or the task ended blocked, so a caller (or an orchestrator) can tell
// "the task moved" from "the task needs me".
func Run(ctx context.Context, opts Options) error {
	out := opts.Out
	if out == nil {
		out = io.Discard
	}

	store, err := state.Load()
	if err != nil {
		return err
	}
	task := store.FindTask(opts.TaskID)
	if task == nil {
		return fmt.Errorf("no task %q in the session store", opts.TaskID)
	}
	rows := store.SessionsForTask(task.ID)
	trunk, roles, err := split(*task, rows)
	if err != nil {
		return err
	}
	// A conflict from an earlier run is still in the tree, so nothing new can
	// merge. Say where it is rather than starting work that cannot land.
	if task.Blocked != "" || git.InMerge(trunk.Path) {
		return fmt.Errorf("%w: %s: resolve the merge in %s", ErrBlocked, task.Blocked, trunk.Path)
	}

	threads, err := resolveAgents(opts.Cfg, roles)
	if err != nil {
		return err
	}

	// Reconcile before starting anything: a role that finished unwatched is
	// merged first, so a conflict left over from a killed run stops the task
	// before this one has children to orphan.
	var fresh []thread
	for _, t := range threads {
		switch action(t.sess) {
		case skipMerged:
			fmt.Fprintf(out, "%s: already merged\n", t.sess.Role)
		case skipLive:
			fmt.Fprintf(out, "%s: already running (pid %d), leaving it alone\n", t.sess.Role, t.sess.RunPID)
		case mergeNow:
			// An earlier supervisor was killed between this role's exit and
			// its merge, so nobody was watching when it finished.
			fmt.Fprintf(out, "%s: finished unwatched, merging\n", t.sess.Role)
			if err := merge(*task, trunk, t.sess, out); err != nil {
				return err
			}
		case start:
			fresh = append(fresh, t)
		}
	}

	pending := make(chan result, len(fresh))
	started := 0
	var wg sync.WaitGroup
	failures := 0

	for _, t := range fresh {
		cmd, cleanup, logPath, err := launch(ctx, task.ID, t)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%s: started (pid %d), log %s\n", t.sess.Role, cmd.Process.Pid, logPath)
		setRun(out, t.sess.Path, state.RunRunning, cmd.Process.Pid)
		started++
		wg.Add(1)
		go func(sess state.Session) {
			defer wg.Done()
			defer cleanup()
			pending <- result{sess: sess, err: cmd.Wait()}
		}(t.sess)
	}

	go func() { wg.Wait(); close(pending) }()

	blocked := ""
	for r := range pending {
		switch {
		case ctx.Err() != nil:
			// Interrupted, not finished: the role's own commits stay on its
			// branch for a later run, but nothing merges on a signal.
			setRun(out, r.sess.Path, state.RunFailed, 0)
			fmt.Fprintf(out, "%s: interrupted\n", r.sess.Role)
			failures++
		case r.err != nil:
			setRun(out, r.sess.Path, state.RunFailed, 0)
			fmt.Fprintf(out, "%s: failed (%v), trunk untouched\n", r.sess.Role, r.err)
			failures++
		default:
			setRun(out, r.sess.Path, state.RunDone, 0)
			if blocked != "" {
				fmt.Fprintf(out, "%s: done, not merged while the task is blocked\n", r.sess.Role)
				continue
			}
			if err := merge(*task, trunk, r.sess, out); err != nil {
				if !errors.Is(err, ErrBlocked) {
					return err
				}
				blocked = err.Error()
			}
		}
	}

	switch {
	case blocked != "":
		return fmt.Errorf("%w: %s", ErrBlocked, blocked)
	case failures > 0:
		return fmt.Errorf("%d of %d roles failed", failures, started)
	}
	return nil
}

// split separates the trunk row from the role rows. The trunk is found by
// branch name from the task record, never by stripping a segment off a role
// branch: every thread is a sibling leaf, so trunk is not a parent of anything.
func split(task state.Task, rows []state.Session) (trunk state.Session, roles []state.Session, err error) {
	found := false
	for _, r := range rows {
		if r.Branch == task.Trunk {
			trunk, found = r, true
			continue
		}
		roles = append(roles, r)
	}
	if !found {
		return trunk, nil, fmt.Errorf("task %s has no worktree on its trunk branch %s", task.ID, task.Trunk)
	}
	if len(roles) == 0 {
		return trunk, nil, fmt.Errorf("task %s has no role worktrees to run", task.ID)
	}
	return trunk, roles, nil
}

// resolveAgents binds each role row to the agent the repo declares for it, and
// rejects the whole run if any of them cannot run unattended. Up front on
// purpose: failing the fourth role after three are already working leaves a
// half-run task nobody asked for.
func resolveAgents(cfg config.Config, roles []state.Session) ([]thread, error) {
	threads := make([]thread, 0, len(roles))
	for _, sess := range roles {
		rc, ok := cfg.Role(sess.Role)
		if !ok {
			return nil, fmt.Errorf("role %q is not declared in this repo any more, so nothing says which agent serves it", sess.Role)
		}
		if err := headless.Supported(rc.AgentConfig); err != nil {
			return nil, fmt.Errorf("role %q: %w", sess.Role, err)
		}
		threads = append(threads, thread{sess: sess, agent: rc.AgentConfig})
	}
	return threads, nil
}

// what a role needs from this run.
type step int

const (
	start step = iota
	skipMerged
	skipLive
	mergeNow
)

// action decides a role's step from the store alone, which is what makes a
// killed supervisor recoverable: the store, plus whether a recorded pid is
// still alive, says everything a fresh run needs.
func action(sess state.Session) step {
	switch sess.Run {
	case state.RunMerged:
		return skipMerged
	case state.RunDone:
		return mergeNow
	case state.RunRunning:
		// A live pid is another supervisor's child, or an orphan of one. Either
		// way it is not ours to wait on, and re-running would put two agents in
		// one worktree. A dead pid means the process exited unwatched.
		if alive(sess.RunPID) {
			return skipLive
		}
		return mergeNow
	}
	return start
}

// alive reports whether the pid exists. Signal 0 checks for the process without
// touching it, which is all the supervisor needs to know.
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// launch opens the role's log and starts its agent in its own worktree. The
// returned cleanup closes the log, and belongs to whoever waits on the process.
func launch(ctx context.Context, taskID string, t thread) (cmd *exec.Cmd, cleanup func(), path string, err error) {
	path, err = logFile(taskID, t.sess.Role)
	if err != nil {
		return nil, nil, "", err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, "", err
	}
	cmd, err = startRole(ctx, t.agent, t.sess.Path, f)
	if err != nil {
		f.Close()
		return nil, nil, "", err
	}
	return cmd, func() { f.Close() }, path, nil
}

// logFile is where an unattended role's JSONL output goes. Nobody is watching
// the run, so the log is the only account of it.
func logFile(taskID, role string) (string, error) {
	dir := filepath.Join(paths.State(), "runs", taskID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, role+".log"), nil
}

// merge lands a finished role on the trunk. No commits is a finished role with
// no output, not a failure. A conflict leaves the trunk worktree exactly as git
// left it and marks the task blocked, because resolving someone's merge on
// their behalf is not norn's call.
func merge(task state.Task, trunk, role state.Session, out io.Writer) error {
	n, err := git.CommitsAhead(trunk.Path, task.Trunk, role.Branch)
	if err != nil {
		return fmt.Errorf("%s: %w", role.Role, err)
	}
	if n == 0 {
		fmt.Fprintf(out, "%s: done, no commits to merge\n", role.Role)
		return nil
	}
	if git.IsDirty(role.Path) {
		fmt.Fprintf(out, "%s: warning, uncommitted changes left in %s are not merged\n", role.Role, role.Path)
	}

	msg := fmt.Sprintf("Merge role %s (%s) into %s", role.Role, role.Branch, task.Trunk)
	if err := git.MergeNoFF(trunk.Path, role.Branch, msg); err != nil {
		reason := fmt.Sprintf("%s: %v", role.Role, err)
		if _, mErr := state.Mutate(func(s *state.Store) bool {
			return s.SetTaskBlocked(task.ID, reason)
		}); mErr != nil {
			return mErr
		}
		fmt.Fprintf(out, "%s: %v\n", role.Role, err)
		return fmt.Errorf("%w: %s", ErrBlocked, reason)
	}

	setRun(out, role.Path, state.RunMerged, 0)
	fmt.Fprintf(out, "%s: merged %d commit(s) into %s\n", role.Role, n, task.Trunk)
	return nil
}

// setRun writes a role's run state immediately rather than at the end of the
// supervisor. Every transition on disk as it happens is what lets a later run
// pick up where a killed one stopped, so a store that will not take the write
// is worth saying out loud even though the role itself is unaffected.
func setRun(out io.Writer, path, run string, pid int) {
	if _, err := state.Mutate(func(s *state.Store) bool { return s.SetRun(path, run, pid) }); err != nil {
		fmt.Fprintf(out, "warning: recording %s for %s: %v\n", run, path, err)
	}
}
