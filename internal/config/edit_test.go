package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func readBack(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestEditPreservesComments(t *testing.T) {
	src := `# top-level comment
worktree_dir: ~/worktrees
agent:
  command: claude # inline note
ai_naming: true
`
	p := writeTemp(t, src)
	e, err := OpenEditor(p)
	if err != nil {
		t.Fatal(err)
	}
	e.SetString([]string{"agent", "command"}, "opencode")
	if err := e.Save(); err != nil {
		t.Fatal(err)
	}

	out := readBack(t, p)
	for _, want := range []string{
		"# top-level comment", // head comment survives
		"# inline note",       // line comment survives
		"command: opencode",   // value changed
		"worktree_dir: ~/worktrees",
		"ai_naming: true",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
}

func TestEditAddsAbsentKeys(t *testing.T) {
	p := writeTemp(t, "worktree_dir: ~/worktrees\n")
	e, _ := OpenEditor(p)
	e.SetString([]string{"agent", "command"}, "opencode") // nested, both absent
	e.SetString([]string{"pr_base"}, "staging")
	e.SetBool([]string{"ai_naming"}, false)
	if err := e.Save(); err != nil {
		t.Fatal(err)
	}

	e2, _ := OpenEditor(p)
	if v, ok := e2.GetString([]string{"agent", "command"}); !ok || v != "opencode" {
		t.Errorf("agent.command = %q, %v", v, ok)
	}
	if v, ok := e2.GetString([]string{"pr_base"}); !ok || v != "staging" {
		t.Errorf("pr_base = %q, %v", v, ok)
	}
	if v, ok := e2.GetString([]string{"ai_naming"}); !ok || v != "false" {
		t.Errorf("ai_naming = %q, %v", v, ok)
	}
}

func TestEditSeq(t *testing.T) {
	p := writeTemp(t, "worktree_dir: ~/worktrees\n")
	e, _ := OpenEditor(p)
	e.SetStringSeq([]string{"base_branches"}, []string{"master", "staging"})
	if err := e.Save(); err != nil {
		t.Fatal(err)
	}
	out := readBack(t, p)
	if !strings.Contains(out, "base_branches:") || !strings.Contains(out, "master") || !strings.Contains(out, "staging") {
		t.Errorf("seq not written:\n%s", out)
	}

	// The written sequence must load back through the normal Config path.
	cfg := DefaultConfig()
	if err := UnmarshalYAML([]byte(out), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.BaseBranches) != 2 || cfg.BaseBranches[0] != "master" {
		t.Errorf("BaseBranches = %v", cfg.BaseBranches)
	}
}

func TestEditQuotesAmbiguousStrings(t *testing.T) {
	p := writeTemp(t, "x: 1\n")
	e, _ := OpenEditor(p)
	e.SetString([]string{"agent", "command"}, "true") // a string that looks boolean
	e.Save()
	out := readBack(t, p)

	// Must round-trip as the string "true", not a bool.
	e2, _ := OpenEditor(p)
	if v, ok := e2.GetString([]string{"agent", "command"}); !ok || v != "true" {
		t.Errorf("ambiguous string not preserved: %q, %v\n%s", v, ok, out)
	}
	cfg := DefaultConfig()
	if err := UnmarshalYAML([]byte(out), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.AgentCommand() != "true" {
		t.Errorf("AgentCommand = %q, want \"true\"", cfg.AgentCommand())
	}
}

func TestEditDelete(t *testing.T) {
	p := writeTemp(t, "pr_base: staging\nai_naming: true\n")
	e, _ := OpenEditor(p)
	e.Delete([]string{"pr_base"})
	e.Save()
	out := readBack(t, p)
	if strings.Contains(out, "pr_base") {
		t.Errorf("pr_base not deleted:\n%s", out)
	}
	if !strings.Contains(out, "ai_naming: true") {
		t.Errorf("unrelated key lost:\n%s", out)
	}
}

func TestEditorCommandSplitsArgs(t *testing.T) {
	t.Setenv("EDITOR", "code --wait")
	cmd := EditorCommand("/tmp/cfg.yaml")
	if got := strings.Join(cmd.Args, " "); got != "code --wait /tmp/cfg.yaml" {
		t.Errorf("args = %q, want %q", got, "code --wait /tmp/cfg.yaml")
	}
}

func TestEditorCommandFallsBackToVi(t *testing.T) {
	t.Setenv("EDITOR", "   ") // whitespace-only counts as unset
	cmd := EditorCommand("/tmp/cfg.yaml")
	if got := strings.Join(cmd.Args, " "); got != "vi /tmp/cfg.yaml" {
		t.Errorf("args = %q, want %q", got, "vi /tmp/cfg.yaml")
	}
}

func TestEditMissingFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "config.yaml") // dir doesn't exist yet
	e, err := OpenEditor(p)
	if err != nil {
		t.Fatal(err)
	}
	e.SetString([]string{"agent", "command"}, "opencode")
	if err := e.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("file not created: %v", err)
	}
}
