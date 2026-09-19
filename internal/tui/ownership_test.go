package tui

import (
	"strings"
	"testing"

	"github.com/sandbye/norn/internal/plan"
)

// A strand has to be told what is not its to write, or it discovers the
// boundary as a conflict at landing.
func TestOwnershipBlockNamesBothSides(t *testing.T) {
	p := plan.Plan{Strands: []plan.Strand{
		{Role: "api", Files: []string{"src/api.ts"}},
		{Role: "web", Files: []string{"src/web.ts", "src/page.tsx"}},
		{Role: "docs", Files: []string{"README.md"}},
	}}

	got := ownershipBlock(p, "api")
	if !strings.Contains(got, "src/api.ts") {
		t.Error("the strand is not told what it owns")
	}
	for _, other := range []string{"web", "src/web.ts", "docs", "README.md"} {
		if !strings.Contains(got, other) {
			t.Errorf("the strand is not told that %q belongs to someone else", other)
		}
	}
	if strings.Contains(got[:strings.Index(got, "Owned by another")], "src/web.ts") {
		t.Error("another strand's file is listed as this strand's own")
	}
	if !strings.Contains(got, "norn tell") {
		t.Error("no way out is offered when the work needs someone else's file")
	}

	// A plan that declares no ownership says nothing rather than an empty
	// heading, since the planner may legitimately leave it out.
	if got := ownershipBlock(plan.Plan{Strands: []plan.Strand{{Role: "api"}}}, "api"); got != "" {
		t.Errorf("a plan without files produced %q", got)
	}
}
