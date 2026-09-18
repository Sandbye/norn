package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const threeRoles = `roles:
  logic:
    agent: claude
  assets:
    agent: codex
    model: gpt-5
  integration:
    command: claude
    integrates: true
`

func loadRoles(t *testing.T, docs ...string) Config {
	t.Helper()
	cfg := DefaultConfig()
	for _, doc := range docs {
		if err := UnmarshalYAML([]byte(doc), &cfg); err != nil {
			t.Fatalf("parse %q: %v", doc, err)
		}
	}
	return cfg
}

func TestRolesParse(t *testing.T) {
	cfg := loadRoles(t, threeRoles)
	if got := len(cfg.Roles); got != 3 {
		t.Fatalf("got %d roles, want 3", got)
	}
	if got := cfg.Roles["logic"].Command; got != "claude" {
		t.Errorf("`agent:` shorthand should set command; got %q", got)
	}
	if got := cfg.Roles["assets"].Model; got != "gpt-5" {
		t.Errorf("assets model = %q, want gpt-5", got)
	}
	if !cfg.Roles["integration"].Integrates {
		t.Error("integration should carry integrates")
	}
	if cfg.Roles["logic"].Integrates {
		t.Error("logic should not carry integrates")
	}
}

// `agent:` is shorthand for `command:`, so a block spelling both is rejected
// rather than one of them silently winning.
func TestRoleRejectsBothSpellings(t *testing.T) {
	cfg := DefaultConfig()
	err := UnmarshalYAML([]byte("roles:\n  logic:\n    agent: codex\n    command: claude\n"), &cfg)
	if err == nil || !strings.Contains(err.Error(), "use one of them") {
		t.Fatalf("err = %v, want one naming the clash", err)
	}
}

// A narrower config file layers onto the broader one per field: the personal
// file marks a role integrating without restating which agent serves it, and
// the untouched roles survive.
func TestRolesMergePerField(t *testing.T) {
	cfg := loadRoles(t, threeRoles, "roles:\n  logic:\n    integrates: true\n  assets:\n    model: opus\n")

	logic := cfg.Roles["logic"]
	if logic.Command != "claude" || !logic.Integrates {
		t.Errorf("logic = %+v, want claude with integrates", logic)
	}
	if got := cfg.Roles["integration"].Command; got != "claude" {
		t.Errorf("untouched role lost its agent: %q", got)
	}
	assets := cfg.Roles["assets"]
	if assets.Command != "codex" || assets.Model != "opus" {
		t.Errorf("assets = %+v, want codex with model opus", assets)
	}
}

func TestRolesReject(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{
			"not a mapping",
			"roles:\n  - logic\n",
			"must be a mapping",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			err := UnmarshalYAML([]byte(tt.yaml), &cfg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string // substring of the error, empty means valid
	}{
		{"no roles at all", "", ""},
		{"exactly one integrates", threeRoles, ""},
		{
			"no integrating role",
			"roles:\n  logic:\n    agent: claude\n  assets:\n    agent: codex\n",
			"roles assets, logic: none sets `integrates: true`",
		},
		{
			"two integrating roles",
			"roles:\n  logic:\n    agent: claude\n    integrates: true\n  assets:\n    agent: codex\n    integrates: true\n",
			"roles assets, logic: each sets `integrates: true`",
		},
		{
			// The name becomes a branch segment, so a space in it would fail
			// halfway through a create instead of at config load.
			"role name with a space",
			"roles:\n  \"back end\":\n    agent: claude\n    integrates: true\n  logic:\n    agent: codex\n",
			"role \"back end\"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := loadRoles(t, tt.yaml)
			err := cfg.Validate()
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("Validate() = %v, want nil", err)
			case tt.want != "" && err == nil:
				t.Fatalf("Validate() = nil, want an error naming the roles")
			case tt.want != "" && !strings.Contains(err.Error(), tt.want):
				t.Fatalf("Validate() = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

func TestRoleNamesOrdersIntegratorFirst(t *testing.T) {
	cfg := loadRoles(t, threeRoles)
	want := []string{"integration", "assets", "logic"}
	got := cfg.RoleNames()
	if len(got) != len(want) {
		t.Fatalf("RoleNames() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("RoleNames() = %v, want %v", got, want)
		}
	}
}

func TestIntegratingRole(t *testing.T) {
	cfg := loadRoles(t, threeRoles)
	name, role, ok := cfg.IntegratingRole()
	if !ok || name != "integration" || role.Command != "claude" {
		t.Fatalf("IntegratingRole() = %q, %+v, %v", name, role, ok)
	}

	if _, _, ok := DefaultConfig().IntegratingRole(); ok {
		t.Error("a repo with no roles has no integrating role")
	}
}

func TestAgentFor(t *testing.T) {
	cfg := loadRoles(t, threeRoles, "agent:\n  command: opencode\n  model: sonnet\n")

	if got := cfg.AgentFor("assets"); got.Command != "codex" || got.Model != "gpt-5" {
		t.Errorf("AgentFor(assets) = %+v, want codex/gpt-5", got)
	}
	// Unknown and empty role names fall back to the repo default, so a task
	// with no role split launches what it always did.
	for _, name := range []string{"", "nonesuch"} {
		if got := cfg.AgentFor(name); got.Command != "opencode" || got.Model != "sonnet" {
			t.Errorf("AgentFor(%q) = %+v, want the default agent", name, got)
		}
	}

	// A role that sets only a model keeps the default command.
	modelOnly := loadRoles(t, "roles:\n  logic:\n    model: opus\n    integrates: true\n")
	if got := modelOnly.AgentFor("logic"); got.Command != "claude" || got.Model != "opus" {
		t.Errorf("AgentFor(logic) = %+v, want claude/opus", got)
	}
}

// Load surfaces an unusable role split instead of starting a task that has no
// agent to merge the work.
func TestLoadValidatesRoles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()

	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, ".norn.yaml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("roles:\n  logic:\n    agent: claude\n  assets:\n    agent: codex\n")
	if _, err := Load(repo); err == nil || !strings.Contains(err.Error(), "integrates") {
		t.Fatalf("Load() = %v, want an error about the missing integrating role", err)
	}

	write(threeRoles)
	cfg, err := Load(repo)
	if err != nil {
		t.Fatalf("Load() = %v, want nil", err)
	}
	if name, _, _ := cfg.IntegratingRole(); name != "integration" {
		t.Errorf("integrating role = %q, want integration", name)
	}

	// A repo with no roles block loads exactly as before.
	write("pr_base: staging\n")
	cfg, err = Load(repo)
	if err != nil || len(cfg.Roles) != 0 || cfg.AgentCommand() != "claude" {
		t.Fatalf("roleless load: err %v, roles %v, agent %q", err, cfg.Roles, cfg.AgentCommand())
	}
}

// A role's args may not repeat a flag norn sets itself: `--sandbox
// danger-full-access` would widen the grant an unattended role runs at, and the
// failure belongs at config load where it is visible, not at spawn.
func TestRoleArgsRejectReservedFlags(t *testing.T) {
	cfg := Config{Roles: Roles{
		"logic":  {AgentConfig: AgentConfig{Command: "claude"}, Integrates: true},
		"design": {AgentConfig: AgentConfig{Command: "codex", Args: []string{"--sandbox", "danger-full-access"}}},
	}}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "--sandbox") {
		t.Fatalf("Validate = %v, want it to reject --sandbox by name", err)
	}

	// The `--flag=value` spelling is the same collision.
	cfg.Roles["design"] = RoleConfig{AgentConfig: AgentConfig{Command: "codex", Args: []string{"--model=gpt-5"}}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "--model") {
		t.Fatalf("Validate = %v, want it to reject --model=... too", err)
	}

	// What args are for: keys norn does not model.
	cfg.Roles["design"] = RoleConfig{AgentConfig: AgentConfig{Command: "codex", Args: []string{"-c", "model_reasoning_effort=low"}}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate rejected a legitimate -c key: %v", err)
	}
}

// A role may wait for another and promise a result. Both are checked at load,
// where a typo is visible, rather than at land, where it would surface as a
// strand that never starts or a gate that never fires.
func TestOrderedRolesValidate(t *testing.T) {
	ok := Config{Roles: Roles{
		"tests":       {AgentConfig: AgentConfig{Command: "claude"}, Expect: ExpectRed},
		"logic":       {AgentConfig: AgentConfig{Command: "claude"}, After: "tests", Expect: ExpectGreen},
		"integration": {AgentConfig: AgentConfig{Command: "claude"}, Integrates: true},
	}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("a valid pipeline was rejected: %v", err)
	}

	unknown := Config{Roles: Roles{
		"logic":       {AgentConfig: AgentConfig{Command: "claude"}, After: "spec"},
		"integration": {AgentConfig: AgentConfig{Command: "claude"}, Integrates: true},
	}}
	if err := unknown.Validate(); err == nil || !strings.Contains(err.Error(), "spec") {
		t.Fatalf("waiting for an undeclared role = %v, want it named", err)
	}

	self := Config{Roles: Roles{
		"logic":       {AgentConfig: AgentConfig{Command: "claude"}, After: "logic"},
		"integration": {AgentConfig: AgentConfig{Command: "claude"}, Integrates: true},
	}}
	if err := self.Validate(); err == nil {
		t.Fatal("a role waiting for itself was accepted, and it would never start")
	}

	bad := Config{Roles: Roles{
		"tests":       {AgentConfig: AgentConfig{Command: "claude"}, Expect: "pink"},
		"integration": {AgentConfig: AgentConfig{Command: "claude"}, Integrates: true},
	}}
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "pink") {
		t.Fatalf("an unknown expect = %v, want it named", err)
	}
}

// A shape is an ordered subset of the declared roles, so the roles stay the one
// declaration and a shape is only a name for a combination of them.
func TestShapes(t *testing.T) {
	cfg := Config{
		Roles: Roles{
			"plan":        {AgentConfig: AgentConfig{Command: "claude"}, Plans: true},
			"tests":       {AgentConfig: AgentConfig{Command: "claude"}, After: "plan", Expect: ExpectRed},
			"integration": {AgentConfig: AgentConfig{Command: "claude"}, Integrates: true},
		},
		Shapes: Shapes{"feature": {"plan", "tests", "integration"}, "fix": {"integration"}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a valid shape was rejected: %v", err)
	}
	if got, ok := cfg.Shape("feature"); !ok || len(got) != 3 {
		t.Fatalf("Shape(feature) = %v, %v", got, ok)
	}
	if got := cfg.ShapeNames(); len(got) != 2 || got[0] != "feature" {
		t.Fatalf("ShapeNames = %v, want them sorted", got)
	}
	if role, ok := cfg.PlanningRole(); !ok || role != "plan" {
		t.Fatalf("PlanningRole = %q, %v", role, ok)
	}

	// A shape naming a role nobody declared is a create that would half-happen.
	cfg.Shapes["broken"] = []string{"plan", "design"}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "design") {
		t.Fatalf("Validate = %v, want it to name the missing role", err)
	}
}

// With a planner in the shape, the integrator has nothing to integrate until
// the plan lands. Spawned alongside it, it designs the fix itself while the
// planner is designing the same fix, and then asks which approach to take.
func TestIntegratorWaitsForThePlanner(t *testing.T) {
	cfg := Config{Roles: Roles{
		"plan":        {AgentConfig: AgentConfig{Command: "claude"}, Plans: true},
		"integration": {AgentConfig: AgentConfig{Command: "claude"}, Integrates: true},
		"logic":       {AgentConfig: AgentConfig{Command: "claude"}},
	}}
	if got := cfg.StartsAfter("integration"); got != "plan" {
		t.Fatalf("StartsAfter(integration) = %q, want plan", got)
	}
	if got := cfg.StartsAfter("logic"); got != "" {
		t.Fatalf("an ordinary role waits for %q", got)
	}
	if got := cfg.StartsAfter("plan"); got != "" {
		t.Fatal("the planner waits for something")
	}

	// A role's own after: wins over the implied one.
	cfg.Roles["integration"] = RoleConfig{AgentConfig: AgentConfig{Command: "claude"}, Integrates: true, After: "logic"}
	if got := cfg.StartsAfter("integration"); got != "logic" {
		t.Fatalf("an explicit after: was overridden: %q", got)
	}

	// With no planner, nothing implies a wait.
	delete(cfg.Roles, "plan")
	cfg.Roles["integration"] = RoleConfig{AgentConfig: AgentConfig{Command: "claude"}, Integrates: true}
	if got := cfg.StartsAfter("integration"); got != "" {
		t.Fatalf("without a planner the integrator waits for %q", got)
	}
}

// A reviewer is someone other than the author, so it cannot also be the role
// that wrote the plan or the one that owns the trunk. It also never starts with
// the rest: it waits for every code strand, which no single `after:` can say.
func TestReviewingRole(t *testing.T) {
	cfg := Config{Roles: Roles{
		"logic":       {AgentConfig: AgentConfig{Command: "claude"}},
		"review":      {AgentConfig: AgentConfig{Command: "claude"}, Reviews: true},
		"integration": {AgentConfig: AgentConfig{Command: "claude"}, Integrates: true},
	}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a valid reviewer was rejected: %v", err)
	}
	if role, ok := cfg.ReviewingRole(); !ok || role != "review" {
		t.Fatalf("ReviewingRole = %q, %v", role, ok)
	}
	if got := cfg.StartsAfter("review"); got == "" {
		t.Fatal("the reviewer would be spawned with the rest, before there is anything to review")
	}
	if got := cfg.StartsAfter("logic"); got != "" {
		t.Fatalf("a code strand waits for %q", got)
	}

	cfg.Roles["review"] = RoleConfig{AgentConfig: AgentConfig{Command: "claude"}, Reviews: true, Integrates: true}
	if err := cfg.Validate(); err == nil {
		t.Fatal("a role that reviews its own integration was accepted")
	}
}

// A shape can leave out the role another role declares `after:`. The wait is
// then not real, and treating it as real leaves the whole task unstartable.
func TestStartsAfterInDropsAbsentRoles(t *testing.T) {
	cfg := Config{Roles: map[string]RoleConfig{
		"plan":        {Plans: true},
		"tests":       {After: "plan"},
		"logic":       {After: "tests"},
		"review":      {Reviews: true},
		"integration": {Integrates: true},
	}}
	small := map[string]bool{"logic": true, "integration": true}

	if got := cfg.StartsAfter("logic"); got != "tests" {
		t.Fatalf("declared wait changed: %q", got)
	}
	if got := cfg.StartsAfterIn("logic", small); got != "" {
		t.Fatalf("logic waits for %q, which this task does not have", got)
	}
	if got := cfg.StartsAfterIn("integration", small); got != "" {
		t.Fatalf("integration waits for %q with no planner in the task", got)
	}

	full := map[string]bool{"plan": true, "tests": true, "logic": true, "review": true, "integration": true}
	if got := cfg.StartsAfterIn("logic", full); got != "tests" {
		t.Fatalf("logic should wait for tests, got %q", got)
	}
	if got := cfg.StartsAfterIn("integration", full); got != "plan" {
		t.Fatalf("integration should wait for the planner, got %q", got)
	}
	if got := cfg.StartsAfterIn("review", map[string]bool{"review": true, "integration": true}); got != "" {
		t.Fatalf("reviewer waits for code strands that do not exist: %q", got)
	}
	if got := cfg.StartsAfterIn("review", full); got != "*" {
		t.Fatalf("reviewer should wait for the code strands, got %q", got)
	}
}
