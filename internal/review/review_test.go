package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderGroupsAndAnchors(t *testing.T) {
	out := Render("main", "Overall solid, two things.", []Comment{
		{Path: "b.go", Line: 12, Label: LabelNitpick, Body: "naming"},
		{Path: "a.go", Line: 40, StartLine: 36, Label: LabelIssue, Blocking: true, Body: "off by one"},
		{Path: "a.go", FileLevel: true, Label: LabelQuestion, Body: "why a new package?"},
		{Path: "a.go", Line: 7, OldSide: true, Label: LabelThought, Body: "this deletion loses the guard"},
	})

	for _, want := range []string{
		"4 comments, 1 blocking · diffed against main",
		"## Summary",
		"Overall solid, two things.",
		"## a.go",
		"**question** — `a.go`",
		"**thought** — `a.go:7 (removed line)`",
		"**issue (blocking)** — `a.go:36-40`",
		"## b.go",
		"**nitpick** — `b.go:12`",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	// Files grouped, a.go before b.go, file-level first within a.go.
	iFile := strings.Index(out, "**question**")
	iLine := strings.Index(out, "**issue (blocking)**")
	iB := strings.Index(out, "## b.go")
	if !(iFile < iLine && iLine < iB) {
		t.Errorf("bad ordering: file-level %d, line %d, b.go %d\n%s", iFile, iLine, iB, out)
	}
}

func TestRenderSingularNoSummary(t *testing.T) {
	out := Render("", "", []Comment{{Path: "a.go", Line: 1, Label: LabelPraise, Body: "nice"}})
	if !strings.Contains(out, "_1 comment_") {
		t.Errorf("want singular header, got:\n%s", out)
	}
	if strings.Contains(out, "## Summary") {
		t.Errorf("summary section rendered for an empty summary:\n%s", out)
	}
}

func TestHeadingDefaultsToThought(t *testing.T) {
	if got := (Comment{}).Heading(); got != "thought" {
		t.Errorf("Heading() = %q, want thought", got)
	}
}

func TestKeysAreUniqueAndCoverLabels(t *testing.T) {
	if len(Keys) != len(Labels) {
		t.Fatalf("Keys has %d entries, Labels %d", len(Keys), len(Labels))
	}
	for _, l := range Labels {
		if Key(l) == "" {
			t.Errorf("label %q has no key", l)
		}
	}
}

func TestWriteCreatesFile(t *testing.T) {
	root := t.TempDir()
	path, err := Write(root, "main", "s", []Comment{{Path: "a.go", Line: 3, Label: LabelTodo, Body: "later"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, File); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "**todo** — `a.go:3`") {
		t.Errorf("unexpected content:\n%s", data)
	}
}
