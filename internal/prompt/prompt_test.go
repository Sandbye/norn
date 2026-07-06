package prompt

import (
	"strings"
	"testing"

	"github.com/sandbye/norn/internal/config"
)

func TestRenderTask(t *testing.T) {
	cfg := config.Config{
		User:    config.User{Name: "Test User", Email: "test@example.com", ClickUpUID: "123"},
		ClickUp: &config.ClickUp{Lists: map[string]string{"Operations": "901513634165"}},
		Verify:  []string{"pnpm check-types", "pnpm check-circular"},
		Setup:   "pnpm cleanup",
	}

	out, err := Render(cfg, "task", "fix the export bug", "master", "")
	if err != nil {
		t.Fatalf("Render task: %v", err)
	}

	checks := []string{
		"# Worktree Session",
		"Test User",
		"test@example.com",
		"Operations",
		`Hint: "fix the export bug"`,
		"pnpm cleanup",
		"pnpm check-types",
		"Startup Procedure",
	}
	for _, c := range checks {
		if !strings.Contains(out, c) {
			t.Errorf("task template missing %q", c)
		}
	}
}

func TestRenderReview(t *testing.T) {
	cfg := config.Config{
		User: config.User{Name: "Test User", Email: "test@example.com"},
	}

	out, err := Render(cfg, "review", "CU-86c98r0j6", "master", "")
	if err != nil {
		t.Fatalf("Render review: %v", err)
	}

	checks := []string{
		"# PR Review Session",
		"Test User",
		`Review hint: "CU-86c98r0j6"`,
		"Review Focus",
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
		{"task with hint", "blah\n2. **Load context.** Hint: \"fix the export bug\"\nmore\n", "fix the export bug"},
		{"review with hint", "1. **Load the task.** Review hint: \"CU-86c98r0j6\"\n", "CU-86c98r0j6"},
		{"no hint provided", "2. **Load context.** No hint provided. Ask the user what to work on.\n", ""},
		{"empty file", "", ""},
		{"unrelated text", "no markers anywhere here", ""},
		{"hint with quotes inside breaks gracefully", `Hint: "fix " bug"`, "fix "}, // matches first balanced pair
	}
	for _, c := range cases {
		got := ExtractHint(c.content)
		if got != c.want {
			t.Errorf("%s: ExtractHint = %q, want %q", c.name, got, c.want)
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

	out, err := Render(cfg, "task", "", "master", "")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(out, "No hint provided. Ask the user what to work on.") {
		t.Error("missing no-hint fallback")
	}
}
