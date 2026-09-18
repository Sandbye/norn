package prompt

import (
	"os"
	"strings"
	"testing"

	"github.com/sandbye/norn/internal/config"
)

// TestMain points the template-override dir at a nonexistent path so tests
// render the built-in templates, not whatever the developer has in
// ~/.config/work/templates.
func TestMain(m *testing.M) {
	SetTemplateDir("/nonexistent-norn-test-template-dir")
	os.Exit(m.Run())
}

func TestRenderTask(t *testing.T) {
	cfg := config.Config{
		User:    config.User{Name: "Test User", Email: "test@example.com", ClickUpUID: "123"},
		ClickUp: &config.ClickUp{Lists: map[string]string{"Todo": "123"}},
		Verify:  []string{"pnpm check-types", "pnpm check-circular"},
		Setup:   "pnpm cleanup",
	}

	out, err := Render(cfg, "task", "fix the export bug", "master", "", nil, nil)
	if err != nil {
		t.Fatalf("Render task: %v", err)
	}

	checks := []string{
		`hint: "fix the export bug"`, // frontmatter
		"base: master",
		"Follow this repo's own conventions",
		`Hint: "fix the export bug"`, // hint block (no task supplied)
		"pnpm cleanup",               // setup
		"pnpm check-types",           // verify
	}
	for _, c := range checks {
		if !strings.Contains(out, c) {
			t.Errorf("task template missing %q", c)
		}
	}
}

func TestRenderTaskWithTaskRef(t *testing.T) {
	out, err := Render(config.Config{}, "task", "#42 fix it", "master", "",
		&TaskRef{ID: "42", Title: "Fix the thing", URL: "https://x/42", Description: "some detail"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"Fix the thing", "`42`", "https://x/42", "some detail", "no need to re-fetch"} {
		if !strings.Contains(out, c) {
			t.Errorf("task-ref render missing %q\n%s", c, out)
		}
	}
}

func TestRenderReview(t *testing.T) {
	cfg := config.Config{
		User: config.User{Name: "Test User", Email: "test@example.com"},
	}

	out, err := Render(cfg, "review", "CU-86c00000", "master", "", nil, nil)
	if err != nil {
		t.Fatalf("Render review: %v", err)
	}

	checks := []string{
		`hint: "CU-86c00000"`, // frontmatter
		`Review hint: "CU-86c00000"`,
		"correctness",
	}
	for _, c := range checks {
		if !strings.Contains(out, c) {
			t.Errorf("review template missing %q", c)
		}
	}
}

func TestExtractHint(t *testing.T) {
	cases := []struct {
		name, content, want string
	}{
		{"frontmatter hint", "---\nhint: \"fix the export bug\"\nbase: master\n---\n# fix the export bug\n", "fix the export bug"},
		{"frontmatter empty hint", "---\nhint: \"\"\nbase: master\n---\nbody\n", ""},
		{"legacy task marker", "blah\n2. **Load context.** Hint: \"fix the export bug\"\nmore\n", "fix the export bug"},
		{"legacy review marker", "1. **Load the task.** Review hint: \"CU-86c00000\"\n", "CU-86c00000"},
		{"no hint provided", "2. **Load context.** No hint provided. Ask the user what to work on.\n", ""},
		{"empty file", "", ""},
		{"unrelated text", "no markers anywhere here", ""},
	}
	for _, c := range cases {
		got := ExtractHint(c.content)
		if got != c.want {
			t.Errorf("%s: ExtractHint = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestExtractNext(t *testing.T) {
	cases := []struct {
		name, content, want string
	}{
		{"state file", "task: Fix login (CU-1)\ngoal: stop the loop\nnext: run the migration\ndone:\n", "run the migration"},
		{"extra spaces", "next:     write the test\n", "write the test"},
		{"tabs", "next:\tpush the branch\n", "push the branch"},
		{"absent", "task: x\ngoal: y\n", ""},
		{"empty next", "next: \n", ""},
		{"empty file", "", ""},
		{"first next wins", "next: first action\nnext: second\n", "first action"},
	}
	for _, c := range cases {
		got := ExtractNext(c.content)
		if got != c.want {
			t.Errorf("%s: ExtractNext = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestExtractBlocked(t *testing.T) {
	cases := []struct{ name, content, want string }{
		{"real blocker", "blocked: waiting on API keys\n", "waiting on API keys"},
		{"none is empty", "blocked: none\n", ""},
		{"None caps", "blocked: None\n", ""},
		{"absent", "task: x\nnext: y\n", ""},
		{"empty value", "blocked: \n", ""},
	}
	for _, c := range cases {
		if got := ExtractBlocked(c.content); got != c.want {
			t.Errorf("%s: ExtractBlocked = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestExtractDone(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    []string
	}{
		{"two items", "done:\n  - wired the handler\n  - added a test\nblocked: none\n", []string{"wired the handler", "added a test"}},
		{"stops at next key", "done:\n  - one\ndecisions:\n  - not this\n", []string{"one"}},
		{"tolerates blank line", "done:\n  - one\n\n  - two\ntouched: x\n", []string{"one", "two"}},
		{"absent", "task: x\nnext: y\n", nil},
		{"empty block", "done:\nblocked: none\n", nil},
	}
	for _, c := range cases {
		got := ExtractDone(c.content)
		if len(got) != len(c.want) {
			t.Errorf("%s: ExtractDone = %v, want %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: ExtractDone[%d] = %q, want %q", c.name, i, got[i], c.want[i])
			}
		}
	}
}

func TestExtractGoal(t *testing.T) {
	cases := []struct{ name, content, want string }{
		{"state file", "task: x\ngoal: ship the SSO flow\nnext: run it\n", "ship the SSO flow"},
		{"absent", "task: x\nnext: y\n", ""},
		{"empty", "goal: \n", ""},
	}
	for _, c := range cases {
		if got := ExtractGoal(c.content); got != c.want {
			t.Errorf("%s: ExtractGoal = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestList(t *testing.T) {
	names := List()
	want := map[string]bool{"task": false, "review": false}
	for _, n := range names {
		if _, ok := want[n]; ok {
			want[n] = true
		}
	}
	for n, seen := range want {
		if !seen {
			t.Errorf("List() missing built-in template %q; got %v", n, names)
		}
	}
	// Sorted + deduped.
	for i := 1; i < len(names); i++ {
		if names[i-1] >= names[i] {
			t.Errorf("List() not sorted/deduped: %v", names)
			break
		}
	}
}

func TestHas(t *testing.T) {
	if !Has("task") {
		t.Error("Has(task) should be true (built-in)")
	}
	if Has("definitely-not-a-template") {
		t.Error("Has(nonexistent) should be false")
	}
	if Has("") {
		t.Error("Has(empty) should be false")
	}
}

func TestResolve(t *testing.T) {
	tests := []struct {
		name     string
		cfg      config.Config
		kind     string
		override string
		want     string
	}{
		{"task default", config.Config{}, "task", "", "task"},
		{"review default", config.Config{}, "review", "", "review"},
		{"valid override wins", config.Config{}, "task", "review", "review"},
		{"unknown override ignored", config.Config{}, "task", "bogus", "task"},
		{"cfg template for task", config.Config{Template: "review"}, "task", "", "review"},
		{"unknown cfg template ignored", config.Config{Template: "bogus"}, "task", "", "task"},
		{"cfg template ignored for review", config.Config{Template: "task"}, "review", "", "review"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Resolve(tt.cfg, tt.kind, tt.override); got != tt.want {
				t.Errorf("Resolve(%+v, %q, %q) = %q, want %q", tt.cfg, tt.kind, tt.override, got, tt.want)
			}
		})
	}
}

func TestRenderNoHint(t *testing.T) {
	cfg := config.Config{
		User: config.User{Name: "Test"},
	}

	out, err := Render(cfg, "task", "", "master", "", nil, nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(out, "No hint provided. Ask the user what to work on.") {
		t.Error("missing no-hint fallback")
	}
}

// The role contract has to survive the template choice. It went missing for a
// real split because the configured template was `detailed`, and the role block
// lived inside `task`: three strands were told they were ordinary worktrees
// working the same issue, and all three started implementing it.
func TestRoleBlockSurvivesAnyTemplate(t *testing.T) {
	cfg := config.Config{}
	role := &RoleRef{Name: "logic", Trunk: "feature/x/trunk", Siblings: []string{"tests"}}

	for _, tmpl := range []string{"task", "checkout"} {
		out, err := Render(cfg, "task", "a hint", "main", tmpl, nil, role)
		if err != nil {
			t.Fatalf("%s: %v", tmpl, err)
		}
		for _, want := range []string{"Your role: logic", "feature/x/trunk", "norn tell", "tests"} {
			if !strings.Contains(out, want) {
				t.Errorf("%s template dropped %q from the brief", tmpl, want)
			}
		}
	}
}

// The integrating role is told not to implement, which is the difference
// between one PR and three strands fighting over the same files.
func TestIntegratingRoleIsToldNotToImplement(t *testing.T) {
	out, err := Render(config.Config{}, "task", "a hint", "main", "task", nil,
		&RoleRef{Name: "integration", Integrates: true, Trunk: "feature/x/trunk"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Do not implement") {
		t.Fatalf("the integrating brief does not say to stay out of the code:\n%s", out)
	}
}

// A worktree that is not part of a split gets no role section at all.
func TestNoRoleNoBlock(t *testing.T) {
	out, err := Render(config.Config{}, "task", "a hint", "main", "task", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Your role") {
		t.Fatalf("a standalone worktree got a role block:\n%s", out)
	}
}

// Global config is shared by every repo, so a project that tracks work on
// GitHub must not be handed another tracker's context.
func TestTrackerDataIsScopedToTheRepo(t *testing.T) {
	cfg := config.Config{
		User:    config.User{Name: "Test User", ClickUpUID: "123"},
		ClickUp: &config.ClickUp{Team: "42", Lists: map[string]string{"backlog": "1"}},
		Tasks:   config.TasksConfig{Provider: "github"},
	}
	got, err := Render(cfg, "task", "a hint", "main", "task", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(got), "clickup") {
		t.Errorf("a github project's brief mentions ClickUp:\n%s", got)
	}

	cfg.Tasks.Provider = "clickup"
	if d := dataFor(cfg); d.ClickUp == nil || d.User.ClickUpUID != "123" {
		t.Error("a clickup project lost its tracker context")
	}
	cfg.Tasks.Provider = "github"
	if d := dataFor(cfg); d.ClickUp != nil || d.User.ClickUpUID != "" {
		t.Error("a github project still carries clickup context")
	}
}

// dataFor is what Render builds, exposed for the test above.
func dataFor(cfg config.Config) Data {
	return Data{User: userFor(cfg), Tracker: tracker(cfg), ClickUp: clickupWithoutToken(cfg)}
}
