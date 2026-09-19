// Package plan is the fan-out a planning strand writes and norn carries out.
//
// One task can need a strand per service, and only someone who has read the
// task knows how many that is. So the planning role decides, and writes the
// plan; norn creates nothing until that plan is landed. The
// judgement is the agent's, the decision to act on it stays yours, and what
// gets created is a file you can read and edit rather than a command an agent
// ran while you were elsewhere.
package plan

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/sandbye/norn/internal/paths"
)

// Dir is where norn keeps plans: its own state directory, never the repo.
//
// A plan written inside the worktree is a file the project's own tooling then
// has an opinion about: eslint linted `.norn/strands.yaml` and `pnpm lint`
// failed on it, in a repo that has never heard of norn. `.git/info/exclude`
// hides such a file from git and from nothing else.
func Dir(taskID string) string {
	return filepath.Join(paths.State(), "plans", taskID)
}

// Path is the plan file for a task.
func Path(taskID string) string { return filepath.Join(Dir(taskID), "strands.yaml") }

// Plan is the fan-out: the strands to create, and why.
type Plan struct {
	// Summary is one line on what the planner concluded, shown on the board so
	// the plan can be judged without opening the file.
	Summary string   `yaml:"summary,omitempty"`
	Strands []Strand `yaml:"strands"`
}

// Strand is one worktree the plan asks for.
type Strand struct {
	// Role becomes the branch's last segment and the name on the rail, so it
	// has to survive being put in a ref.
	Role string `yaml:"role"`
	// Agent is the agent command for this strand; empty takes the repo default.
	Agent string `yaml:"agent,omitempty"`
	// Model overrides the agent's default model for this strand.
	Model string `yaml:"model,omitempty"`
	// After names another strand in this plan that must land first.
	After string `yaml:"after,omitempty"`
	// Expect is "red" or "green": what the repo's verify must report before
	// this strand may land.
	Expect string `yaml:"expect,omitempty"`
	// Files are the paths this strand may write, and nobody else may. Globs
	// allowed. A path claimed twice is a merge conflict the plan could have
	// prevented, so it is rejected before anything is created.
	Files []string `yaml:"files,omitempty"`
	// Brief is what this strand owns, in the planner's words. It is appended to
	// the generated worktree brief, and it is the part worth reading: it says
	// which files and which service belong to this strand and nobody else.
	Brief string `yaml:"brief"`
}

// rolePattern is what a role name may be: it becomes a branch segment.
var rolePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// Read loads a task's plan.
func Read(taskID string) (*Plan, error) {
	path := Path(taskID)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no plan at %s: the planning strand has not written one yet", path)
		}
		return nil, err
	}
	var p Plan
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("%s is not valid yaml: %w", path, err)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// Validate rejects a plan norn cannot carry out. Checked before anything is
// created, because half a fan-out is worse than none: the strands that exist
// start working while the ones that failed are missing from the task.
func (p Plan) Validate() error {
	if len(p.Strands) == 0 {
		return fmt.Errorf("the plan names no strands")
	}
	seen := map[string]bool{}
	for i, s := range p.Strands {
		switch {
		case s.Role == "":
			return fmt.Errorf("strand %d has no role", i+1)
		case !rolePattern.MatchString(s.Role):
			return fmt.Errorf("role %q becomes a branch segment, so use letters, digits, dot, dash or underscore", s.Role)
		case seen[s.Role]:
			return fmt.Errorf("role %q appears twice, and two worktrees cannot share a branch", s.Role)
		case strings.TrimSpace(s.Brief) == "":
			return fmt.Errorf("strand %q has no brief, so it would start knowing only its name", s.Role)
		}
		seen[s.Role] = true
	}
	if err := p.checkOwnership(); err != nil {
		return err
	}
	for _, s := range p.Strands {
		switch {
		case s.After == "":
		case s.After == s.Role:
			return fmt.Errorf("strand %q waits for itself", s.Role)
		case !seen[s.After]:
			return fmt.Errorf("strand %q waits for %q, which this plan does not create", s.Role, s.After)
		}
		switch s.Expect {
		case "", "red", "green":
		default:
			return fmt.Errorf("strand %q expects %q, and the only values are red and green", s.Role, s.Expect)
		}
	}
	return nil
}

// checkOwnership rejects a plan where two strands may write the same path.
//
// Two agents editing one file in parallel is the failure the split exists to
// avoid: it surfaces as a conflict at landing, after both have done the work,
// and neither of them could have seen it coming from inside its own worktree.
//
// Reading a file is not owning it, so this only compares what each strand
// declares it will write.
func (p Plan) checkOwnership() error {
	owner := map[string]string{}
	for _, s := range p.Strands {
		for _, f := range s.Files {
			f = strings.TrimSpace(f)
			if f == "" {
				continue
			}
			if first, taken := owner[f]; taken {
				return fmt.Errorf("%s and %s both claim %s: one strand owns a path, or they conflict at landing", first, s.Role, f)
			}
			owner[f] = s.Role
		}
	}
	return nil
}

// Waiting reports the strands that start only once other strands have landed,
// so the caller can spawn the rest now and these later.
func (p Plan) Waiting() map[string]string {
	out := map[string]string{}
	for _, s := range p.Strands {
		if s.After != "" {
			out[s.Role] = s.After
		}
	}
	return out
}
