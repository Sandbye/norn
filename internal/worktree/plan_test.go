package worktree

import (
	"reflect"
	"testing"

	"github.com/sandbye/norn/internal/config"
)

func rolesCfg() config.Config {
	return config.Config{Roles: config.Roles{
		"integration": {Integrates: true},
		"logic":       {},
		"assets":      {},
	}}
}

// A repo that declares no roles keeps the branch name it always had.
func TestPlanWithoutRolesIsOneWorktree(t *testing.T) {
	got := Plan(config.Config{}, "feature/x/CU-1", []string{"logic"})
	want := []Thread{{Branch: "feature/x/CU-1"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Plan() = %+v, want %+v", got, want)
	}
}

// Declaring roles is not the same as splitting: a create that picks none still
// makes exactly one worktree.
func TestPlanWithoutPickedRolesIsOneWorktree(t *testing.T) {
	got := Plan(rolesCfg(), "feature/x/CU-1", nil)
	want := []Thread{{Branch: "feature/x/CU-1"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Plan() = %+v, want %+v", got, want)
	}
}

// Picking one role means that role plus whoever merges it, and the trunk takes
// a leaf of its own so the role branches can be its siblings.
func TestPlanAddsTheIntegratingRole(t *testing.T) {
	got := Plan(rolesCfg(), "feature/x/CU-1", []string{"logic"})
	want := []Thread{
		{Role: "integration", Integrates: true, Branch: "feature/x/CU-1/trunk"},
		{Role: "logic", Branch: "feature/x/CU-1/logic"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Plan() = %+v, want %+v", got, want)
	}
}

// The integrating role alone is not a split — there is nothing to merge.
func TestPlanIntegratingRoleAloneIsOneWorktree(t *testing.T) {
	got := Plan(rolesCfg(), "feature/x/CU-1", []string{"integration"})
	want := []Thread{{Branch: "feature/x/CU-1"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Plan() = %+v, want %+v", got, want)
	}
}

// Trunk first, then alphabetical, whatever order the create asked in.
func TestPlanOrdersTrunkFirst(t *testing.T) {
	got := Plan(rolesCfg(), "feature/x", []string{"logic", "assets"})
	want := []string{"feature/x/trunk", "feature/x/assets", "feature/x/logic"}
	var branches []string
	for _, t := range got {
		branches = append(branches, t.Branch)
	}
	if !reflect.DeepEqual(branches, want) {
		t.Fatalf("branches = %v, want %v", branches, want)
	}
	if !got[0].Integrates {
		t.Fatalf("Threads[0] = %+v, want the integrating role", got[0])
	}
}

// A role the repo never declared is reported, not silently dropped: building
// fewer worktrees than were asked for is worse than refusing the create.
func TestUnknownRoles(t *testing.T) {
	got := UnknownRoles(rolesCfg(), []string{"logic", "design", "copy"})
	want := []string{"copy", "design"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("UnknownRoles() = %v, want %v", got, want)
	}
	if got := UnknownRoles(rolesCfg(), []string{"logic", "assets"}); got != nil {
		t.Fatalf("UnknownRoles() = %v, want nil", got)
	}
}
