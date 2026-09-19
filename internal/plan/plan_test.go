package plan

import (
	"os"
	"strings"
	"testing"
)

// write puts a plan where norn keeps them, which is its own state directory
// and never the repository: a plan inside a worktree gets linted, formatted or
// committed by tooling that has never heard of norn.
func write(t *testing.T, body string) string {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	taskID := "task1"
	if err := os.MkdirAll(Dir(taskID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(taskID), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return taskID
}

// The plan is what a planning strand hands back: which strands to create, what
// each owns, and which of them wait for another.
func TestReadPlan(t *testing.T) {
	dir := write(t, `
summary: three services and a frontend
strands:
  - role: payments
    agent: claude
    brief: the charge path and its queue writer
  - role: frontend
    agent: codex
    after: payments
    brief: the states the new endpoint can report
  - role: tests
    expect: red
    brief: cover the queue writer before it exists
`)
	p, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Strands) != 3 || p.Summary == "" {
		t.Fatalf("plan = %+v", p)
	}
	if got := p.Waiting(); len(got) != 1 || got["frontend"] != "payments" {
		t.Fatalf("waiting = %v, want frontend after payments", got)
	}
}

// Every rejection here is a fan-out that would half-happen: strands created and
// working while the rest are missing from the task.
func TestPlanRejections(t *testing.T) {
	cases := map[string]string{
		"no strands":      "strands: []",
		"no role":         "strands:\n  - brief: something",
		"bad role":        "strands:\n  - role: pay ments\n    brief: x",
		"duplicate role":  "strands:\n  - role: pay\n    brief: x\n  - role: pay\n    brief: y",
		"no brief":        "strands:\n  - role: pay",
		"waits for ghost": "strands:\n  - role: pay\n    brief: x\n    after: nobody",
		"waits for self":  "strands:\n  - role: pay\n    brief: x\n    after: pay",
		"bad expect":      "strands:\n  - role: pay\n    brief: x\n    expect: pink",
	}
	for name, body := range cases {
		if _, err := Read(write(t, body)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// A strand that has not written a plan says so, rather than reading as an empty
// one and creating nothing without explanation.
func TestMissingPlan(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	_, err := Read("nosuchtask")
	if err == nil || !strings.Contains(err.Error(), "strands.yaml") {
		t.Fatalf("missing plan = %v, want it to name the file", err)
	}
}

// The plan never lives in the repository, which is the whole reason it moved:
// eslint linted it and `pnpm lint` failed on a file the project knows nothing
// about.
func TestPlanLivesOutsideTheRepo(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if got := Path("abc"); strings.Contains(got, ".norn") {
		t.Fatalf("plan path %q is still repo-shaped", got)
	}
}

// Two strands that may write the same path conflict at landing, after both
// have done the work. The plan is where that is still cheap to catch.
func TestPlanRejectsTwoOwnersOfOnePath(t *testing.T) {
	p := Plan{Strands: []Strand{
		{Role: "api", Brief: "the endpoint", Files: []string{"src/api.ts", "src/shared.ts"}},
		{Role: "web", Brief: "the page", Files: []string{"src/web.ts", "src/shared.ts"}},
	}}
	err := p.Validate()
	if err == nil {
		t.Fatal("a plan with two owners of src/shared.ts was accepted")
	}
	for _, want := range []string{"api", "web", "src/shared.ts"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}

	// Reading the same file is not owning it, so a plan that declares no
	// overlap stays valid.
	p.Strands[1].Files = []string{"src/web.ts"}
	if err := p.Validate(); err != nil {
		t.Fatalf("a plan with distinct owners was rejected: %v", err)
	}
}
