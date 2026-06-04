package prompt

import (
	"strings"
	"testing"

	"github.com/sandbye/work/internal/config"
)

func TestRenderTask(t *testing.T) {
	cfg := config.Config{
		User:    config.User{Name: "Test User", Email: "test@example.com", ClickUpUID: "123"},
		ClickUp: &config.ClickUp{Lists: map[string]string{"Operations": "901513634165"}},
		Verify:  []string{"pnpm check-types", "pnpm check-circular"},
		Setup:   "pnpm cleanup",
	}

	out, err := Render(cfg, "task", "fix the export bug", "master")
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

	out, err := Render(cfg, "review", "CU-86c98r0j6", "master")
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

func TestRenderNoHint(t *testing.T) {
	cfg := config.Config{
		User: config.User{Name: "Test"},
	}

	out, err := Render(cfg, "task", "", "master")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(out, "No hint provided. Ask the user what to work on.") {
		t.Error("missing no-hint fallback")
	}
}
