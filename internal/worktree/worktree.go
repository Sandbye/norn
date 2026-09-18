// Package worktree creates the worktrees one `norn create` produces: a single
// one, or a trunk plus one per role when the task is split across agents.
//
// It exists because create has two entry points (the `norn create` verb and the
// TUI's New tab) that must produce byte-identical worktrees. Before roles there
// was little to share; a split has branch naming, ordering, brief wiring, store
// rows and unwind-on-failure to keep in agreement, and two copies of that is
// two behaviours.
package worktree

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sandbye/norn/internal/config"
	"github.com/sandbye/norn/internal/git"
	"github.com/sandbye/norn/internal/plan"
	"github.com/sandbye/norn/internal/prompt"
	"github.com/sandbye/norn/internal/state"
)

// trunkLeaf is the last segment of the integrating role's branch. The role
// branches are `<branch>/<role>`, so the trunk cannot also be `<branch>`: git
// stores a ref as a file, and `refs/heads/x` being a file is exactly what stops
// `refs/heads/x/logic` from existing. Giving the trunk a leaf of its own makes
// every thread a sibling and the conflict goes away.
const trunkLeaf = "trunk"

// Request is one create, whether it makes a single worktree or a split task.
type Request struct {
	Kind     string // task | review
	Hint     string
	Branch   string // the resolved branch name (trunk's prefix when split)
	Base     string // branch to fork from
	Template string
	Roles    []string // roles picked for this task; fewer than two means no split
	Task     *prompt.TaskRef
}

// Thread is one worktree of a create: which role owns it and where it landed.
type Thread struct {
	Role       string // "" when the create made a single worktree
	Integrates bool
	Branch     string
	Path       string
}

// Result is what a create produced. Threads[0] is the trunk (or the only
// worktree), which is the one the caller hands the agent off to.
type Result struct {
	TaskID   string // "" when the create made a single worktree
	Threads  []Thread
	Warnings []string
}

// Trunk is the thread the caller launches into and cd's to.
func (r Result) Trunk() Thread {
	if len(r.Threads) == 0 {
		return Thread{}
	}
	return r.Threads[0]
}

// Split reports whether the create produced a role split rather than one
// worktree.
func (r Result) Split() bool { return r.TaskID != "" }

// Plan lays out the threads a create produces, trunk first, without touching
// git. Fewer than two effective roles is one worktree on the plain branch name,
// which is what every create was before roles existed: a repo declaring roles
// only splits when a create asks it to.
//
// The integrating role is always in the set. Picking `logic` alone still means
// "logic plus whoever merges it", because a role branch with no trunk to merge
// into is not a split, it is a worktree with a suffix.
func Plan(cfg config.Config, branch string, roles []string) []Thread {
	integrating, _, ok := cfg.IntegratingRole()
	if !ok {
		return []Thread{{Branch: branch}}
	}
	picked := map[string]bool{integrating: true}
	for _, r := range roles {
		if _, known := cfg.Role(r); known {
			picked[r] = true
		}
	}
	if len(picked) < 2 {
		return []Thread{{Branch: branch}}
	}
	// cfg.RoleNames() is integrating-first then alphabetical, so the trunk
	// lands at Threads[0] without a second ordering rule to keep in sync.
	threads := make([]Thread, 0, len(picked))
	for _, name := range cfg.RoleNames() {
		if !picked[name] {
			continue
		}
		leaf := name
		if name == integrating {
			leaf = trunkLeaf
		}
		threads = append(threads, Thread{
			Role:       name,
			Integrates: name == integrating,
			Branch:     branch + "/" + leaf,
		})
	}
	return threads
}

// UnknownRoles reports the names a create asked for that the repo does not
// declare, so the caller can reject the whole create instead of silently
// building fewer worktrees than were asked for.
func UnknownRoles(cfg config.Config, roles []string) []string {
	var unknown []string
	for _, r := range roles {
		if _, ok := cfg.Role(r); !ok {
			unknown = append(unknown, r)
		}
	}
	sort.Strings(unknown)
	return unknown
}

// Create builds every worktree the request calls for and records them as one
// task. It is all-or-nothing: a failure on the third worktree removes the first
// two and their branches, and the session store is written once at the end, so
// a half-built task never reaches disk in either place.
func Create(cfg config.Config, repoRoot string, req Request) (Result, error) {
	threads := Plan(cfg, req.Branch, req.Roles)
	res := Result{Threads: make([]Thread, 0, len(threads))}
	split := len(threads) > 1
	trunkBranch := threads[0].Branch
	// Minted before the worktrees exist, because a planning role's brief has to
	// name the task's plan path and that path is keyed by this id.
	if split {
		res.TaskID = state.NewTaskID()
	}

	// Unwind only what this call created: CreateWorktreeFrom reports a reused
	// branch or worktree as neither, and removing one of those would delete a
	// thread the failed create never owned.
	var built []git.CreateOutcome
	unwind := func() {
		for i := len(built) - 1; i >= 0; i-- {
			git.DiscardNewWorktree(repoRoot, built[i], res.Threads[i].Branch)
		}
	}

	for _, t := range threads {
		// The trunk forks from the base on origin; a role forks from the trunk
		// cut moments ago, which exists locally and nowhere else yet.
		startRef, remote := req.Base, true
		if split && !t.Integrates {
			startRef, remote = trunkBranch, false
		}
		// The trunk is pushed because it is what a PR and `norn diff` look at;
		// a strand is not, because nothing off this machine needs it.
		out, err := git.CreateWorktreeFrom(repoRoot, cfg.WorktreeDir, t.Branch, startRef, remote, !split || t.Integrates)
		if err != nil {
			unwind()
			return Result{}, fmt.Errorf("%s: %w", t.Branch, err)
		}
		built = append(built, out)
		t.Path = out.Path
		res.Threads = append(res.Threads, t)

		if err := git.SymlinkEnvFiles(repoRoot, t.Path); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s: env symlinks incomplete: %v", t.Branch, err))
		}
		if err := writeBrief(cfg, req, t, threads, trunkBranch, res.TaskID); err != nil {
			unwind()
			return Result{}, err
		}
	}

	if err := record(repoRoot, req, res); err != nil {
		unwind()
		return Result{}, err
	}
	return res, nil
}

// writeBrief renders and writes the worktree's `.worktree.md`. Failing to write
// it is fatal to the create rather than a warning: the brief is what the agent
// reads on arrival, so launching without one hides the failure behind a session
// that then invents its own scope.
func writeBrief(cfg config.Config, req Request, t Thread, threads []Thread, trunkBranch, taskID string) error {
	var role *prompt.RoleRef
	if t.Role != "" {
		rc, _ := cfg.Role(t.Role)
		_, planned := cfg.PlanningRole()
		role = &prompt.RoleRef{
			Name:       t.Role,
			Integrates: t.Integrates,
			Plans:      rc.Plans,
			Reviews:    rc.Reviews,
			TestFirst:  rc.TestFirst,
			Planned:    planned,
			PlanPath:   plan.Path(taskID),
			Trunk:      trunkBranch,
			Siblings:   siblings(threads, t.Role),
		}
	}
	// A role brief states the branch it actually forked from, so its diff
	// baseline is the trunk rather than the base the trunk came from.
	base := req.Base
	if role != nil && !t.Integrates {
		base = trunkBranch
	}
	tmpl := prompt.Resolve(cfg, req.Kind, req.Template)
	text, err := prompt.Render(cfg, req.Kind, req.Hint, base, tmpl, req.Task, role)
	if err != nil {
		return fmt.Errorf("render brief for %s: %w", t.Branch, err)
	}
	path := filepath.Join(t.Path, ".worktree.md")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return fmt.Errorf("write brief for %s: %w", t.Branch, err)
	}
	return nil
}

// siblings lists the other roles in the task, so a brief can name what is being
// built in parallel and is therefore not this worktree's to touch.
func siblings(threads []Thread, self string) []string {
	var out []string
	for _, t := range threads {
		if t.Role != self {
			out = append(out, t.Role)
		}
	}
	return out
}

// record writes the task and its session rows in one Mutate. One write, at the
// end: a row per worktree written as it is built would leave rows pointing at
// worktrees a later failure removed.
func record(repoRoot string, req Request, res Result) error {
	repo := git.OriginRepoName(repoRoot)
	now := time.Now()
	_, err := state.Mutate(func(store *state.Store) bool {
		if res.TaskID != "" {
			store.UpsertTask(state.Task{
				ID:        res.TaskID,
				Repo:      repo,
				Goal:      req.Hint,
				Trunk:     res.Trunk().Branch,
				CreatedAt: now,
			})
		}
		for _, t := range res.Threads {
			// The branch reliably carries #<id> after naming; the hint is the
			// fallback for a branch format that drops it.
			clickup := git.ClickUpID(t.Branch)
			if clickup == "" {
				clickup = git.ClickUpID(req.Hint)
			}
			store.UpsertByPath(state.Session{
				ID:             state.MakeID(repo, t.Branch),
				Repo:           repo,
				Branch:         t.Branch,
				Kind:           req.Kind,
				Path:           t.Path,
				ClickUpID:      clickup,
				Status:         state.StatusActive,
				StartedAt:      now,
				LastActivityAt: now,
				TaskID:         res.TaskID,
				Role:           t.Role,
			})
		}
		return true
	})
	return err
}

// AddStrand creates one more worktree in an existing task, off its trunk.
//
// Tasks are not fixed at create any more: a planning strand decides how many
// services a task touches, and the strands it asks for are made here when you
// land that plan. Brief is the planner's own words about what this strand owns,
// appended to the generated brief, since "which files are mine" is the thing a
// template cannot know.
func AddStrand(cfg config.Config, repoRoot string, task state.Task, role, brief string) (Thread, error) {
	if role == "" {
		return Thread{}, fmt.Errorf("a strand needs a role")
	}
	branch := strings.TrimSuffix(task.Trunk, "/"+trunkLeaf) + "/" + role
	out, err := git.CreateWorktreeFrom(repoRoot, cfg.WorktreeDir, branch, task.Trunk, false, false)
	if err != nil {
		return Thread{}, fmt.Errorf("%s: %w", branch, err)
	}
	t := Thread{Role: role, Branch: branch, Path: out.Path}

	if err := git.SymlinkEnvFiles(repoRoot, t.Path); err != nil {
		// Not fatal: the strand can work, it just lacks the env files, and
		// unwinding a worktree over that would lose more than it saves.
		_ = err
	}
	if err := writeStrandBrief(cfg, task, t, brief); err != nil {
		git.DiscardNewWorktree(repoRoot, out, branch)
		return Thread{}, err
	}

	repo := git.OriginRepoName(repoRoot)
	now := time.Now()
	if _, err := state.Mutate(func(store *state.Store) bool {
		store.UpsertByPath(state.Session{
			ID: state.MakeID(repo, branch), Repo: repo, Branch: branch, Kind: "task",
			Path: t.Path, ClickUpID: git.ClickUpID(branch), Status: state.StatusActive,
			StartedAt: now, LastActivityAt: now, TaskID: task.ID, Role: role,
		})
		return true
	}); err != nil {
		git.DiscardNewWorktree(repoRoot, out, branch)
		return Thread{}, err
	}
	return t, nil
}

// writeStrandBrief renders the worktree brief for a planned strand and appends
// what the planner said it owns.
func writeStrandBrief(cfg config.Config, task state.Task, t Thread, brief string) error {
	role := &prompt.RoleRef{Name: t.Role, Trunk: task.Trunk}
	text, err := prompt.Render(cfg, "task", task.Goal, task.Trunk, prompt.Resolve(cfg, "task", ""), nil, role)
	if err != nil {
		return fmt.Errorf("render brief for %s: %w", t.Branch, err)
	}
	if s := strings.TrimSpace(brief); s != "" {
		text = strings.TrimRight(text, "\n") + "\n\n## What this strand owns\n\n" + s + "\n"
	}
	return os.WriteFile(filepath.Join(t.Path, ".worktree.md"), []byte(text), 0o644)
}
