package config

import (
	"fmt"
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
}

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
// (command, args, model) work unchanged, and an explicit `command` wins. Keys
// the block omits keep whatever an earlier config file set.
func (r *RoleConfig) UnmarshalYAML(node *yaml.Node) error {
	v := struct {
		Agent      string   `yaml:"agent"`
		Command    string   `yaml:"command"`
		Args       []string `yaml:"args"`
		Model      string   `yaml:"model"`
		Integrates bool     `yaml:"integrates"`
	}{Args: r.Args, Model: r.Model, Integrates: r.Integrates}
	if err := node.Decode(&v); err != nil {
		return err
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

// Validate reports config that parses but cannot be acted on. Roles are the
// only such rule today: the split needs one agent to merge the work, and norn
// will not pick that agent for you.
func (c Config) Validate() error {
	if len(c.Roles) == 0 {
		return nil
	}
	var integrating []string
	for _, name := range c.RoleNames() {
		if c.Roles[name].Integrates {
			integrating = append(integrating, name)
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

func sortedNames(roles Roles) []string {
	names := make([]string, 0, len(roles))
	for name := range roles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
