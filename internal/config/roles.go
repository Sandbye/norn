package config

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Named roles for a task split across several agents. A role is a name the
// person splitting the work already thinks in (the logic role, the assets role),
// bound to the agent that serves it. The repo's top-level `agent:`
// stays the default, so a repo that declares no roles behaves as it always did.
//
//	roles:
//	  logic:
//	    agent: claude
//	  assets:
//	    agent: codex
//	  integration:
//	    agent: claude
//	    integrates: true

// Shapes are named task shapes: an ordered list of declared role names, so a
// kind of work you do repeatedly ("a feature", "a bug fix") is picked once at
// create time rather than reassembled from roles every time.
//
//	shapes:
//	  feature: [plan, tests, backend, frontend, integration]
type Shapes map[string][]string

// Shape returns the roles of a named shape.
func (c Config) Shape(name string) ([]string, bool) {
	roles, ok := c.Shapes[name]
	return roles, ok
}

// ShapeNames lists the declared shapes, alphabetically, for a picker.
func (c Config) ShapeNames() []string {
	names := make([]string, 0, len(c.Shapes))
	for name := range c.Shapes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// StartsAfter is the role a given role waits for, taking the planner into
// account.
//
// A role's own `after:` wins. Otherwise the integrating role waits for the
// planner when there is one: until the plan lands there is nothing to
// integrate, and an integrator spawned early designs the fix itself, in
// parallel with the planner doing the same thing.
func (c Config) StartsAfter(role string) string {
	rc, ok := c.Roles[role]
	if !ok {
		return ""
	}
	if rc.After != "" {
		return rc.After
	}
	if planner, has := c.PlanningRole(); has && rc.Integrates && planner != role {
		return planner
	}
	if rc.Reviews {
		// Waits for every code strand, which no single name can express; the
		// landing path starts it when the last one lands.
		return "*"
	}
	return ""
}

// ReviewingRole returns the role that reviews the combined work, if declared.
func (c Config) ReviewingRole() (string, bool) {
	for _, name := range c.RoleNames() {
		if c.Roles[name].Reviews {
			return name, true
		}
	}
	return "", false
}

// IntegratingRoleName is the name of the role that owns the trunk.
func (c Config) IntegratingRoleName() (string, bool) {
	name, _, ok := c.IntegratingRole()
	return name, ok
}

// PlanningRole returns the role that decides the task's shape, if one is
// declared. At most one: two planners would each write the same file.
func (c Config) PlanningRole() (string, bool) {
	for _, name := range c.RoleNames() {
		if c.Roles[name].Plans {
			return name, true
		}
	}
	return "", false
}

// Roles is the declared role map. It merges per field rather than per role, so
// a personal config can mark one role integrating without restating which agent
// serves it, the way every other config key layers.
type Roles map[string]RoleConfig

// RoleConfig is one named role: the agent that serves it, plus whether it is
// the role that integrates the others' work.
type RoleConfig struct {
	AgentConfig `yaml:",inline" json:",inline"`

	// Integrates marks the single role that merges the other roles' output.
	// Exactly one role in the map carries it; Validate rejects zero or several.
	Integrates bool `yaml:"integrates,omitempty" json:"integrates,omitempty"`

	// After names a role this one waits for. It is not spawned at create; it
	// starts when that role lands, so its branch forks from a trunk that
	// already contains the other's work. Empty means it starts with the rest.
	After string `yaml:"after,omitempty" json:"after,omitempty"`

	// Plans marks a role that decides the shape of the rest of the task instead
	// of writing code. It reads the task and writes `.norn/strands.yaml` naming
	// the strands it wants; landing it is what creates them, so the fan-out is
	// the agent's judgement and yours, never the agent's alone.
	Plans bool `yaml:"plans,omitempty" json:"plans,omitempty"`

	// Reviews marks a role that reads the combined trunk and reports, instead
	// of writing product code. It starts once every code strand has landed,
	// which is the first moment the whole change exists in one place.
	Reviews bool `yaml:"reviews,omitempty" json:"reviews,omitempty"`

	// Expect is what the repo's verify must report for this role's work to be
	// allowed onto the trunk: "red" for a strand whose job is a failing test,
	// "green" for one that has to leave the tree working. Empty gates nothing.
	Expect string `yaml:"expect,omitempty" json:"expect,omitempty"`
}

// Expect values. Red is the half of test-first a machine can check: a test that
// passes before the implementation exists proves nothing.
const (
	ExpectRed   = "red"
	ExpectGreen = "green"
)

// UnmarshalYAML merges each block into the role already loaded from a broader
// config file, keeping keys the narrower file left out.
func (r *Roles) UnmarshalYAML(node *yaml.Node) error {
	if node.Tag == "!!null" {
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: roles must be a mapping of role name to agent", node.Line)
	}
	if *r == nil {
		*r = Roles{}
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		var name string
		if err := node.Content[i].Decode(&name); err != nil {
			return err
		}
		role := (*r)[name]
		if err := node.Content[i+1].Decode(&role); err != nil {
			return err
		}
		(*r)[name] = role
	}
	return nil
}

// UnmarshalYAML accepts `agent: <command>` as shorthand for `command:`, since a
// role block reads as "which agent serves this role". The full AgentConfig keys
// (command, args, model) work unchanged. A block spelling both is rejected
// rather than resolved silently, because a precedence rule nobody reads is how
// you end up launching an agent you did not pick. Keys the block omits keep
// whatever an earlier config file set.
func (r *RoleConfig) UnmarshalYAML(node *yaml.Node) error {
	v := struct {
		Agent      string   `yaml:"agent"`
		Command    string   `yaml:"command"`
		Args       []string `yaml:"args"`
		Model      string   `yaml:"model"`
		Integrates bool     `yaml:"integrates"`
		After      string   `yaml:"after"`
		Expect     string   `yaml:"expect"`
		Plans      bool     `yaml:"plans"`
		Reviews    bool     `yaml:"reviews"`
	}{Args: r.Args, Model: r.Model, Integrates: r.Integrates, After: r.After, Expect: r.Expect, Plans: r.Plans, Reviews: r.Reviews}
	if err := node.Decode(&v); err != nil {
		return err
	}
	if v.Command != "" && v.Agent != "" {
		return fmt.Errorf("line %d: role sets both `agent:` and `command:`, so use one of them", node.Line)
	}
	command := v.Command
	if command == "" {
		command = v.Agent
	}
	if command == "" {
		command = r.Command
	}
	*r = RoleConfig{
		AgentConfig: AgentConfig{Command: command, Args: v.Args, Model: v.Model},
		Integrates:  v.Integrates,
		After:       v.After,
		Expect:      v.Expect,
		Plans:       v.Plans,
		Reviews:     v.Reviews,
	}
	return nil
}

// RoleNames lists the declared roles in a stable order, integrating role first
// and the rest alphabetical, so every view agrees on the order.
func (c Config) RoleNames() []string {
	names := make([]string, 0, len(c.Roles))
	for name := range c.Roles {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		a, b := c.Roles[names[i]], c.Roles[names[j]]
		if a.Integrates != b.Integrates {
			return a.Integrates
		}
		return names[i] < names[j]
	})
	return names
}

// Role returns the named role's config.
func (c Config) Role(name string) (RoleConfig, bool) {
	role, ok := c.Roles[name]
	return role, ok
}

// IntegratingRole returns the role that merges the others' work. Validate
// guarantees there is exactly one whenever roles are declared, so a false here
// means the repo declared no roles at all.
func (c Config) IntegratingRole() (string, RoleConfig, bool) {
	for _, name := range c.RoleNames() {
		if role := c.Roles[name]; role.Integrates {
			return name, role, true
		}
	}
	return "", RoleConfig{}, false
}

// AgentFor returns the agent that serves a role, falling back to the repo's
// default agent for an empty or unknown role name, so a task with no role split
// launches exactly what it launched before roles existed.
func (c Config) AgentFor(role string) AgentConfig {
	r, ok := c.Roles[role]
	if !ok {
		return c.Agent
	}
	// A role may set only model or args, so fill the command from the default.
	if r.Command == "" {
		r.Command = c.AgentCommand()
	}
	return r.AgentConfig
}

// roleNamePattern is what a role name may be. A role name becomes the last
// segment of a branch, so anything git refuses in a ref would surface as a
// worktree-add failure halfway through a create rather than as bad config.
var roleNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// ReservedArgs are the flags norn sets itself when it runs a role unattended.
// A role's `args:` may not repeat one: `--sandbox danger-full-access` would
// silently widen a grant norn does not make configurable, and a second `--json`
// or `--output-format` can make the event stream unparseable, which surfaces as
// a role that failed for no visible reason.
//
// Listed here rather than in internal/headless because config cannot import it
// (headless imports config). A test in that package asserts the list still
// covers every flag it sets, so the two cannot drift apart silently.
var ReservedArgs = []string{
	"-p", "--output-format", "--verbose", "--permission-mode", "--append-system-prompt",
	"--json", "--sandbox", "--approve-for-me", "-m", "--model",
}

// Validate reports config that parses but cannot be acted on: the split needs
// one agent to merge the work and norn will not pick that agent for you, a role
// name has to survive being put in a branch, and a role's args have to leave
// norn's own flags alone.
func (c Config) Validate() error {
	if len(c.Roles) == 0 {
		return nil
	}
	for _, name := range sortedNames(c.Roles) {
		if !roleNamePattern.MatchString(name) {
			return fmt.Errorf("role %q: a role name becomes a branch segment, so use letters, digits, dot, dash or underscore", name)
		}
		role := c.Roles[name]
		if role.After != "" {
			if _, ok := c.Roles[role.After]; !ok {
				return fmt.Errorf("role %q waits for %q, which this repo does not declare", name, role.After)
			}
			if role.After == name {
				return fmt.Errorf("role %q waits for itself", name)
			}
		}
		if role.Reviews && (role.Integrates || role.Plans) {
			return fmt.Errorf("role %q reviews as well as planning or integrating, and a reviewer has to be someone other than the author", name)
		}
		if role.Plans && role.Integrates {
			return fmt.Errorf("role %q both plans and integrates, and those are opposite jobs: one decides the shape, the other closes it", name)
		}
		switch role.Expect {
		case "", ExpectRed, ExpectGreen:
		default:
			return fmt.Errorf("role %q: expect is %q, and the only values are %q and %q", name, role.Expect, ExpectRed, ExpectGreen)
		}
		if flag := reservedArg(c.Roles[name].Args); flag != "" {
			return fmt.Errorf("role %q: `args:` sets %s, which norn sets itself when it runs the role (use args for what norn does not model, like `-c` keys)", name, flag)
		}
	}
	var integrating []string
	for _, name := range c.RoleNames() {
		if c.Roles[name].Integrates {
			integrating = append(integrating, name)
		}
	}
	var planners []string
	for _, name := range c.RoleNames() {
		if c.Roles[name].Plans {
			planners = append(planners, name)
		}
	}
	if len(planners) > 1 {
		sort.Strings(planners)
		return fmt.Errorf("roles %s each set `plans: true`, and they would write the same file over each other", strings.Join(planners, ", "))
	}
	for shape, roles := range c.Shapes {
		if len(roles) == 0 {
			return fmt.Errorf("shape %q lists no roles", shape)
		}
		for _, r := range roles {
			if _, ok := c.Roles[r]; !ok {
				return fmt.Errorf("shape %q names role %q, which this repo does not declare", shape, r)
			}
		}
	}
	switch len(integrating) {
	case 1:
		return nil
	case 0:
		return fmt.Errorf("roles %s: none sets `integrates: true`, so nothing merges the others' work (mark exactly one)", strings.Join(sortedNames(c.Roles), ", "))
	default:
		sort.Strings(integrating)
		return fmt.Errorf("roles %s: each sets `integrates: true`, and only one role may", strings.Join(integrating, ", "))
	}
}

// reservedArg returns the first argument that collides with a flag norn owns,
// in both the `--flag value` and `--flag=value` spellings, or "" when none do.
func reservedArg(args []string) string {
	for _, a := range args {
		name := a
		if i := strings.IndexByte(a, '='); i > 0 {
			name = a[:i]
		}
		for _, r := range ReservedArgs {
			if name == r {
				return r
			}
		}
	}
	return ""
}

func sortedNames(roles Roles) []string {
	names := make([]string, 0, len(roles))
	for name := range roles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
