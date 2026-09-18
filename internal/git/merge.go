package git

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrMergeConflict is a merge that stopped with conflicted paths. It is kept
// distinct from every other merge failure because it is the one that leaves the
// worktree mid-merge: norn stops there rather than resolving or aborting, so
// the tree the person opens is the one git left.
var ErrMergeConflict = errors.New("merge conflict")

// ErrTrunkDirty is a merge refused before it started, because the trunk
// worktree has uncommitted changes. Merging into it would mix them into the
// merge commit or fail halfway.
var ErrTrunkDirty = errors.New("trunk worktree has uncommitted changes")

// CommitsAhead counts the commits on branch that trunk does not have. Zero
// means the role wrote nothing worth merging, which is a finished role with no
// output rather than a failure.
func CommitsAhead(dir, trunk, branch string) (int, error) {
	out, err := cmdOutput(dir, "git", "rev-list", "--count", trunk+".."+branch)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("rev-list --count %s..%s: %q: %w", trunk, branch, out, err)
	}
	return n, nil
}

// MergeNoFF merges branch into whatever is checked out at trunkPath, always as
// a merge commit. --no-ff on purpose: a role's work stays a visible unit in
// trunk's history even when trunk has not moved since the fork.
//
// A conflict returns ErrMergeConflict with git's own output and leaves the
// worktree mid-merge.
func MergeNoFF(trunkPath, branch, message string) error {
	if IsDirty(trunkPath) {
		return fmt.Errorf("%w: %s", ErrTrunkDirty, trunkPath)
	}
	out, err := captureRun(trunkPath, "git", "merge", "--no-ff", "--no-edit", "-m", message, branch)
	if err == nil {
		return nil
	}
	if InMerge(trunkPath) {
		return fmt.Errorf("%w: %s", ErrMergeConflict, strings.TrimSpace(out))
	}
	if o := strings.TrimSpace(out); o != "" {
		return fmt.Errorf("merge %s into %s: %w: %s", branch, trunkPath, err, o)
	}
	return fmt.Errorf("merge %s into %s: %w", branch, trunkPath, err)
}

// InMerge reports whether the worktree is sitting in an unfinished merge. It is
// how a later norn run tells "nobody has resolved this yet" from "the conflict
// is gone", without keeping a handle on the process that hit it.
func InMerge(wtPath string) bool {
	return cmdRun(wtPath, "git", "rev-parse", "--verify", "--quiet", "MERGE_HEAD") == nil
}
