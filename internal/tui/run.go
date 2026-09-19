package tui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sandbye/norn/internal/claude"
	"github.com/sandbye/norn/internal/config"
	"github.com/sandbye/norn/internal/git"
	"github.com/sandbye/norn/internal/paths"
	"github.com/sandbye/norn/internal/plan"
	"github.com/sandbye/norn/internal/runlog"
	"github.com/sandbye/norn/internal/state"
	"github.com/sandbye/norn/internal/strand"
	"github.com/sandbye/norn/internal/worktree"
)

// runStartedMsg reports whether the detached supervisor got off the ground.
// Only the start is reported: what the roles then do reaches the dashboard
// through the session store, which is where the supervisor writes every
// transition anyway.
type runStartedMsg struct {
	taskID string
	err    error
}

// startRoleRunCmd spawns a live strand per role of the task, each in its own
// detached tmux session, and returns at once.
//
// tmux owns the agents, not norn: quitting the TUI leaves every strand working,
// and the pane is a client that comes and goes. That is also why this does not
// wait for anything. A strand's exit is read back from tmux when the rail asks.
// planStrand is a role's entry in its task's plan, when a planner created it.
//
// A plan carries the same three things a declared role does: what it runs on,
// what it waits for, and what verify must say. Those were read only at creation
// time, so a plan-created strand's `after:` was forgotten the moment norn
// restarted and its `expect:` was never checked at all.
func planStrand(taskID, role string) (plan.Strand, bool) {
	p, err := plan.Read(taskID)
	if err != nil {
		return plan.Strand{}, false
	}
	for _, s := range p.Strands {
		if s.Role == role {
			return s, true
		}
	}
	return plan.Strand{}, false
}

// waitFor is the strand this one starts after: the plan's answer when the plan
// created it, the config's otherwise.
func waitFor(cfg config.Config, taskID, role string, present map[string]bool) string {
	if s, ok := planStrand(taskID, role); ok {
		if s.After != "" && present[s.After] {
			return s.After
		}
		return ""
	}
	return cfg.StartsAfterIn(role, present)
}

// agentFor is the agent a strand runs, honouring a plan's per-strand override.
func agentFor(cfg config.Config, taskID, role string) config.AgentConfig {
	agent := cfg.AgentFor(role)
	if s, ok := planStrand(taskID, role); ok {
		if s.Agent != "" {
			agent.Command = s.Agent
		}
		if s.Model != "" {
			agent.Model = s.Model
		}
	}
	return agent
}

// hasLanded reports whether a role's work is already on the trunk. R is also
// the catch-up key: a strand whose predecessor landed while norn was not
// watching must start now rather than wait for a landing that already happened.
func hasLanded(store *state.Store, taskID, role string) bool {
	for _, s := range store.SessionsForTask(taskID) {
		if s.Role == role {
			return s.Run == state.RunMerged
		}
	}
	return false
}

// whyStuck turns a fast-forward failure into something worth reading on a
// status line: what the strand is holding, not what git printed.
func whyStuck(err error) string {
	switch {
	case errors.Is(err, git.ErrWorktreeDirty):
		return "has uncommitted changes"
	case errors.Is(err, git.ErrHasOwnCommits):
		return "has commits the trunk does not, so it needs landing first"
	default:
		return "could not move: " + err.Error()
	}
}

// rolesOf is the set of roles a task actually has, which is what a wait is
// checked against.
func rolesOf(sessions []state.Session) map[string]bool {
	present := map[string]bool{}
	for _, s := range sessions {
		if s.Role != "" {
			present[s.Role] = true
		}
	}
	return present
}

func startRoleRunCmd(cfg config.Config, taskID string, cols, rows int) tea.Cmd {
	return func() tea.Msg {
		if !strand.Available() {
			return runStartedMsg{taskID: taskID, err: strand.ErrNoTmux}
		}
		store, err := state.Load()
		if err != nil {
			return runStartedMsg{taskID: taskID, err: err}
		}
		if store.FindTask(taskID) == nil {
			return runStartedMsg{taskID: taskID, err: fmt.Errorf("no task %q in the session store", taskID)}
		}

		present := rolesOf(store.SessionsForTask(taskID))
		started := 0
		var stalled []string
		for _, sess := range store.SessionsForTask(taskID) {
			// The trunk is a strand like any other: it is where a conflict gets
			// resolved and where the PR is opened, and both have to be possible
			// without leaving norn.
			if sess.Role == "" {
				continue
			}
			if strand.Alive(taskID, sess.Role) {
				st, err := strand.Read(taskID, sess.Role)
				if err != nil || st.Running {
					continue // working: re-spawning would lose what it is doing
				}
				// The pane is dead, which is what `/exit` leaves behind. Put the
				// agent back in it, resuming its own session rather than
				// starting one that has never seen this task.
				//
				// Bring it up to the trunk first, the way a fresh spawn does:
				// a strand that exited before its predecessor landed would
				// otherwise resume on the checkout it left behind. Best effort,
				// because a strand that has committed is not fast-forwardable
				// and merging its work is a decision, not a side effect.
				if task := store.FindTask(taskID); task != nil {
					_ = git.FastForward(sess.Path, task.Trunk)
				}
				agent := agentFor(cfg, taskID, sess.Role)
				argv := resumeArgs(cfg, agent, sess.Path)
				if err := strand.Respawn(taskID, sess.Role, sess.Path, argv); err != nil {
					return runStartedMsg{taskID: taskID, err: err}
				}
				setStrandRunning(sess.Path)
				started++
				continue
			}
			after := waitFor(cfg, taskID, sess.Role, present)
			if after != "" && !hasLanded(store, taskID, after) {
				// Waiting on another strand: it starts when that one lands, so
				// its branch forks from a trunk that already holds the work it
				// is supposed to build on.
				continue
			}
			// Catch-up: this strand waited for one that has since landed, so its
			// branch has to come up to the trunk before the agent sees it.
			if after != "" {
				if task := store.FindTask(taskID); task != nil {
					if err := git.FastForward(sess.Path, task.Trunk); err != nil {
						// Report it, but keep going: one strand nobody can move
						// is not a reason to leave the rest of the task unstarted.
						stalled = append(stalled, sess.Role+" "+whyStuck(err))
						continue
					}
				}
			}
			agent := agentFor(cfg, taskID, sess.Role)
			argv := strandCmd(cfg, agent, sess.Path, agent.Model).Args
			if err := strand.Spawn(taskID, sess.Role, sess.Path, argv, cols, rows); err != nil {
				return runStartedMsg{taskID: taskID, err: err}
			}
			setStrandRunning(sess.Path)
			started++
		}
		switch {
		case started == 0 && len(stalled) > 0:
			return runStartedMsg{taskID: taskID, err: fmt.Errorf("nothing started: %s", strings.Join(stalled, "; "))}
		case started == 0:
			return runStartedMsg{taskID: taskID, err: errors.New("nothing to start: every strand is already running or waits for one that is")}
		case len(stalled) > 0:
			return runStartedMsg{taskID: taskID, err: fmt.Errorf("started %d · waiting: %s", started, strings.Join(stalled, "; "))}
		}
		return runStartedMsg{taskID: taskID}
	}
}

// setStrandRunning records that a strand is live, so the rail says so and a
// restart knows to look for its session.
func setStrandRunning(path string) {
	_, _ = state.Mutate(func(s *state.Store) bool { return s.SetRun(path, state.RunRunning, 0) })
}

// runLogDir is where a task's run logs live: the supervisor's own progress
// next to the per-role output it writes.
func runLogDir(taskID string) string {
	return filepath.Join(paths.State(), "runs", taskID)
}

// runLog opens the supervisor's log. Nobody is watching a detached run, so it
// needs somewhere to say which role merged and which failed.
func runLog(taskID string) (*os.File, error) {
	dir := runLogDir(taskID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(filepath.Join(dir, "run.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}

// logLoadedMsg carries a role's rendered log back to the dashboard.
type logLoadedMsg struct {
	role   string
	events []runlog.Event
	err    error
}

// logTailLines is how much of a run the viewer keeps. A rail of recent
// activity, not a transcript: the file stays on disk for the whole story.
const logTailLines = 200

// loadRunLogCmd reads a role's log and renders it. Re-run on every dashboard
// tick while the viewer is open, which is what makes a running role legible:
// the file is being appended to as the agent works.
func loadRunLogCmd(taskID, role string) tea.Cmd {
	return func() tea.Msg {
		events, err := runlog.Tail(roleLogPath(taskID, role), logTailLines)
		return logLoadedMsg{role: role, events: events, err: err}
	}
}

// roleLogPath is where a role's agent writes its JSONL, the same path the
// supervisor opens for it.
func roleLogPath(taskID, role string) string {
	return filepath.Join(runLogDir(taskID), role+".log")
}

// landedMsg reports the outcome of landing one strand on the trunk.
type landedMsg struct {
	taskID  string
	role    string
	commits int
	created []string // strands a landed plan asked for
	blocked bool
	err     error
}

// landStrandCmd merges a finished strand into its task's trunk.
//
// A keypress rather than something norn does on exit: with a live pane, exit
// also means "I quit to look at something", and a merge commit is not undone
// casually. The merge itself is #71's, unchanged.
func landStrandCmd(cfg config.Config, row dashRow) tea.Cmd {
	return func() tea.Msg {
		store, err := state.Load()
		if err != nil {
			return landedMsg{role: row.Role, err: err}
		}
		task := store.FindTask(row.TaskID)
		if task == nil {
			return landedMsg{role: row.Role, err: fmt.Errorf("no task %q in the session store", row.TaskID)}
		}
		trunk := store.FindTaskTrunk(row.TaskID)
		if trunk == nil {
			return landedMsg{role: row.Role, err: fmt.Errorf("task %s has no trunk worktree", row.TaskID)}
		}
		if row.Branch == task.Trunk {
			return landedMsg{role: row.Role, err: errors.New("the trunk is what strands land on")}
		}

		// A planning strand lands its decision, not its code. Nothing is merged:
		// norn reads the plan from that worktree, and requiring a commit made
		// the planner commit `.norn/strands.yaml`, which then rode the trunk
		// into the PR as orchestration metadata in somebody else's repo.
		if rc, ok := cfg.Role(row.Role); ok && rc.Plans {
			created, err := carryOutPlan(cfg, row, *task)
			if err != nil {
				return landedMsg{taskID: row.TaskID, role: row.Role, err: err}
			}
			_, _ = state.Mutate(func(s *state.Store) bool { return s.SetRun(row.Path, state.RunMerged, 0) })
			return landedMsg{taskID: row.TaskID, role: row.Role, created: created}
		}

		n, err := git.CommitsAhead(trunk.Path, task.Trunk, row.Branch)
		if err != nil {
			return landedMsg{role: row.Role, err: err}
		}
		if n == 0 {
			return landedMsg{role: row.Role, err: errors.New("nothing to land: no commits on this strand")}
		}
		if err := checkExpect(cfg, row); err != nil {
			return landedMsg{role: row.Role, err: err}
		}

		msg := fmt.Sprintf("Merge role %s (%s) into %s", row.Role, row.Branch, task.Trunk)
		if err := git.MergeNoFF(trunk.Path, row.Branch, msg); err != nil {
			reason := fmt.Sprintf("%s: %v", row.Role, err)
			_, _ = state.Mutate(func(s *state.Store) bool { return s.SetTaskBlocked(row.TaskID, reason) })
			return landedMsg{role: row.Role, blocked: true, err: err}
		}
		_, _ = state.Mutate(func(s *state.Store) bool { return s.SetRun(row.Path, state.RunMerged, 0) })

		return landedMsg{taskID: row.TaskID, role: row.Role, commits: n}
	}
}

// expectOf is what verify must report before this strand may land: the plan's
// word when a plan created it, the role's declaration otherwise.
func expectOf(cfg config.Config, taskID, role string) string {
	if s, ok := planStrand(taskID, role); ok {
		return s.Expect
	}
	if rc, ok := cfg.Role(role); ok {
		return rc.Expect
	}
	return ""
}

// checkExpect runs the repo's verify in the strand's own worktree and holds the
// merge unless the result is what the role promised.
//
// This is the half of red-first a machine can check: a test strand whose suite
// passes before the implementation exists has written a test that proves
// nothing, and letting it land is how the whole sequence becomes decoration.
func checkExpect(cfg config.Config, row dashRow) error {
	expect := expectOf(cfg, row.TaskID, row.Role)
	if expect == "" {
		return nil
	}
	if len(cfg.Verify) == 0 {
		return fmt.Errorf("%s expects %s, but this repo declares no verify commands to check it with", row.Role, expect)
	}
	// The gate reports what it is on: a suite takes minutes, and silence for
	// minutes is indistinguishable from a hang.
	defer clearVerifyStep(row.Role)
	passed, failing, out := runVerify(row.Path, cfg.Verify, func(cmd string, i, n int) {
		setVerifyStep(row.Role, cmd, i, n)
	})
	switch {
	case expect == config.ExpectGreen && !passed:
		return fmt.Errorf("%s must leave the tree green, and `%s` fails:\n%s", row.Role, failing, out)
	case expect == config.ExpectRed && passed:
		return fmt.Errorf("%s must leave a failing test, and every verify command passes: a test that does not fail proves nothing", row.Role)
	}
	return nil
}

// runVerify runs the configured commands in dir, stopping at the first failure.
// The output of that one is returned, since it is the only one worth reading.
func runVerify(dir string, cmds []string, onStep func(cmd string, index, total int)) (passed bool, failing, output string) {
	for i, c := range cmds {
		if onStep != nil {
			onStep(c, i+1, len(cmds))
		}
		cmd := exec.Command("sh", "-c", c)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			return false, c, lastLines(string(out), 12)
		}
	}
	return true, "", ""
}

// lastLines keeps the tail of a command's output: a failing suite's useful part
// is at the end, and a full log does not belong in a notice.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// spawnWaitingCmd starts the strands that were waiting for the one that just
// landed. This is what makes the sequence real rather than advisory: the next
// role's branch forks from a trunk that already contains its predecessor's
// work, so a test written before the implementation is a fact of the history.
func spawnWaitingCmd(cfg config.Config, taskID, landed string, cols, rows int) tea.Cmd {
	return func() tea.Msg {
		store, err := state.Load()
		if err != nil {
			return runStartedMsg{taskID: taskID, err: err}
		}
		present := rolesOf(store.SessionsForTask(taskID))
		started := 0
		for _, sess := range store.SessionsForTask(taskID) {
			if waitFor(cfg, taskID, sess.Role, present) != landed || strand.Alive(taskID, sess.Role) {
				continue
			}
			// The worktree was created when the plan was accepted, so its branch
			// forked from a trunk without the work it waited for. Move it up
			// before the agent reads a single file.
			if task := store.FindTask(taskID); task != nil {
				if err := git.FastForward(sess.Path, task.Trunk); err != nil {
					return runStartedMsg{taskID: taskID, err: fmt.Errorf("%s: %w", sess.Role, err)}
				}
			}
			agent := agentFor(cfg, taskID, sess.Role)
			argv := strandCmd(cfg, agent, sess.Path, agent.Model).Args
			if err := strand.Spawn(taskID, sess.Role, sess.Path, argv, cols, rows); err != nil {
				return runStartedMsg{taskID: taskID, err: err}
			}
			setStrandRunning(sess.Path)
			started++
		}
		if started == 0 {
			return nil
		}
		return runStartedMsg{taskID: taskID}
	}
}

// carryOutPlan creates the strands a planning strand asked for, and starts the
// ones that are not waiting on another.
//
// Validation happens before anything is created: half a fan-out is worse than
// none, because the strands that exist start working while the missing ones are
// silently absent from the task.
func carryOutPlan(cfg config.Config, row dashRow, task state.Task) ([]string, error) {
	rc, ok := cfg.Role(row.Role)
	if !ok || !rc.Plans {
		return nil, nil
	}
	p, err := plan.Read(row.TaskID)
	if err != nil {
		return nil, err
	}

	repoRoot := git.MainCheckout(row.Path)
	if repoRoot == "" {
		repoRoot = row.Path
	}
	waiting := p.Waiting()
	var created []string
	for _, s := range p.Strands {
		t, err := worktree.AddStrand(cfg, repoRoot, task, s.Role, s.Brief)
		if err != nil {
			return created, fmt.Errorf("creating %s: %w", s.Role, err)
		}
		created = append(created, s.Role)
		if waiting[s.Role] != "" {
			continue // starts when the strand it waits for lands
		}
		agent := cfg.AgentFor(s.Role)
		if s.Agent != "" {
			agent.Command = s.Agent
		}
		if s.Model != "" {
			agent.Model = s.Model
		}
		if err := strand.Spawn(task.ID, s.Role, t.Path, strandCmd(cfg, agent, t.Path, agent.Model).Args, 120, 30); err != nil {
			return created, fmt.Errorf("starting %s: %w", s.Role, err)
		}
		setStrandRunning(t.Path)
	}
	if len(created) > 0 {
		// The planner's job ends here: its decision has been carried out, and
		// an agent left alive with nothing to do still holds a session, a rail
		// row and a model that will answer if something types at it.
		_ = strand.Kill(task.ID, row.Role)
	}
	return created, nil
}

// tellIntegrator says that a strand has landed on the trunk, and whether any
// are still outstanding.
//
// The integrating agent lives in the trunk worktree and cannot see norn press
// the key: without this it keeps its picture from the last time it looked and
// asks to perform a merge that has already happened.
func tellIntegrator(cfg config.Config, store *state.Store, task state.Task, landed string, commits int) {
	role, ok := cfg.IntegratingRoleName()
	if !ok || role == landed {
		return
	}
	var pending []string
	for _, sess := range store.SessionsForTask(task.ID) {
		if sess.Role == "" || sess.Role == role || sess.Role == landed || sess.Run == state.RunMerged {
			continue
		}
		if rc, known := cfg.Role(sess.Role); known && rc.Plans {
			continue // the planner never lands code
		}
		pending = append(pending, sess.Role)
	}

	msg := fmt.Sprintf("norn merged %s into %s with --no-ff (%d commit(s)). ", landed, task.Trunk, commits)
	if len(pending) > 0 {
		msg += "Still to land: " + strings.Join(pending, ", ") + ". Wait for them."
	} else {
		msg += "Every strand has landed. Verify the combined tree and open the PR."
	}
	_ = strand.Send(task.ID, role, msg)
}

// spawnReviewer starts the reviewing role once every code strand has landed.
//
// Not `after: <role>`, because what it waits for is not one strand but all of
// them: the first moment the whole change exists in one place is the first
// moment a review means anything.
func spawnReviewer(cfg config.Config, store *state.Store, task state.Task, landed string) {
	role, ok := cfg.ReviewingRole()
	if !ok || strand.Alive(task.ID, role) {
		return
	}
	var reviewer *state.Session
	for _, sess := range store.SessionsForTask(task.ID) {
		switch {
		case sess.Role == role:
			s := sess
			reviewer = &s
		case sess.Role == "" || sess.Branch == task.Trunk || sess.Role == landed:
			// The trunk is what they land on, and the strand that just landed
			// is accounted for by the caller.
		case sess.Run == state.RunMerged:
		default:
			if rc, known := cfg.Role(sess.Role); known && (rc.Plans || rc.Integrates) {
				continue // neither lands code
			}
			return // a code strand is still outstanding
		}
	}
	if reviewer == nil {
		return
	}
	agent := cfg.AgentFor(role)
	if err := strand.Spawn(task.ID, role, reviewer.Path, strandCmd(cfg, agent, reviewer.Path, agent.Model).Args, 120, 30); err != nil {
		return
	}
	setStrandRunning(reviewer.Path)
	_ = strand.Send(task.ID, role, fmt.Sprintf(
		"Every strand has landed on %s. Review the combined change and report; send anything a strand should fix to that strand with `norn tell`.", task.Trunk))
}

// resumeArgs is the command that puts an agent back in a strand, continuing the
// conversation it already had when one exists.
//
// A restart that forgets is nearly useless here: the strand has a branch, a
// brief and an hour of context, and starting fresh means re-deriving all of it.
func resumeArgs(cfg config.Config, agent config.AgentConfig, dir string) []string {
	if agent.Command == "" || agent.Command == "claude" {
		if id := claude.SessionIDFor(dir); id != "" {
			args := []string{"claude", "--resume", id}
			if effort := cfg.EffortFor(agent.Model); effort != "" {
				args = append(args, "--effort", effort)
			}
			return append(args, "--permission-mode", "auto", "--prompt-suggestions", "false")
		}
	}
	return strandCmd(cfg, agent, dir, agent.Model).Args
}

// approvePRCmd tells the integrating strand that the person has reviewed the
// combined change and it may open the pull request.
//
// A message rather than a flag in the store, because the integrator is an agent
// in a terminal: what it acts on is what arrives in its session.
func approvePRCmd(cfg config.Config, taskID string) tea.Cmd {
	return func() tea.Msg {
		role, ok := cfg.IntegratingRoleName()
		if !ok {
			return landedMsg{taskID: taskID, err: errors.New("this repo declares no integrating role")}
		}
		if err := strand.Send(taskID, role, "The PR is approved: I have reviewed the combined change. Open it now, following this repository's pull request template."); err != nil {
			return landedMsg{taskID: taskID, role: role, err: err}
		}
		return nil
	}
}
