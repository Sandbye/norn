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
