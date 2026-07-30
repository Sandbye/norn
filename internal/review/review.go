// Package review renders a local code review to a markdown file inside the
// worktree, so a human can review a branch and hand the result to the agent
// without a PR (and without making the repo public).
//
// Comment bodies follow conventionalcomments.org: a label, an optional
// (blocking) decoration, then the body.
package review

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Dir is the per-worktree directory norn writes into.
const Dir = ".norn"

// File is the review's path relative to the worktree root.
const File = Dir + "/review.md"

// Label is a conventional-comment label.
type Label string

const (
	LabelIssue      Label = "issue"
	LabelSuggestion Label = "suggestion"
	LabelNitpick    Label = "nitpick"
	LabelQuestion   Label = "question"
	LabelTodo       Label = "todo"
	LabelPraise     Label = "praise"
	LabelThought    Label = "thought"
	LabelChore      Label = "chore"
)

// Labels is the pick order shown in the TUI.
var Labels = []Label{
	LabelIssue, LabelSuggestion, LabelNitpick, LabelQuestion,
	LabelTodo, LabelPraise, LabelThought, LabelChore,
}

// Keys maps the picker keystroke to its label. One key per label, all distinct
// first letters except thought/chore, which take h and o.
var Keys = map[string]Label{
	"i": LabelIssue,
	"s": LabelSuggestion,
	"n": LabelNitpick,
	"q": LabelQuestion,
	"t": LabelTodo,
	"p": LabelPraise,
	"h": LabelThought,
	"o": LabelChore,
}

// Key returns the picker keystroke for a label ("" when unknown).
func Key(l Label) string {
	for k, v := range Keys {
		if v == l {
			return k
		}
	}
	return ""
}

// Comment is one review comment. FileLevel comments carry no line anchor.
type Comment struct {
	Path      string
	Line      int  // end line of the range, or the single line
	StartLine int  // 0 when single-line
	OldSide   bool // line numbers refer to the pre-image (a removed line)
	FileLevel bool
	Label     Label
	Blocking  bool
	Body      string
}

// anchor renders the `path:line` reference the agent jumps to.
func (c Comment) anchor() string {
	if c.FileLevel {
		return c.Path
	}
	loc := fmt.Sprintf("%s:%d", c.Path, c.Line)
	if c.StartLine > 0 && c.StartLine != c.Line {
		lo, hi := c.StartLine, c.Line
		if lo > hi {
			lo, hi = hi, lo
		}
		loc = fmt.Sprintf("%s:%d-%d", c.Path, lo, hi)
	}
	if c.OldSide {
		loc += " (removed line)"
	}
	return loc
}

// Heading renders the conventional-comment heading, e.g. "issue (blocking)".
func (c Comment) Heading() string {
	l := c.Label
	if l == "" {
		l = LabelThought
	}
	if c.Blocking {
		return string(l) + " (blocking)"
	}
	return string(l)
}

// Render builds the review markdown. base labels what the branch was diffed
// against ("main", "HEAD (uncommitted)"); summary is the reviewer's overall
// note and may be empty.
func Render(base, summary string, comments []Comment) string {
	sorted := make([]Comment, len(comments))
	copy(sorted, comments)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Path != sorted[j].Path {
			return sorted[i].Path < sorted[j].Path
		}
		if sorted[i].FileLevel != sorted[j].FileLevel {
			return sorted[i].FileLevel // file-level first within a file
		}
		return sorted[i].Line < sorted[j].Line
	})

	blocking := 0
	for _, c := range sorted {
		if c.Blocking {
			blocking++
		}
	}

	var b strings.Builder
	b.WriteString("# Code review\n\n")
	head := fmt.Sprintf("%d comment", len(sorted))
	if len(sorted) != 1 {
		head += "s"
	}
	if blocking > 0 {
		head += fmt.Sprintf(", %d blocking", blocking)
	}
	if base != "" {
		head += " · diffed against " + base
	}
	b.WriteString("_" + head + "_\n")

	if s := strings.TrimSpace(summary); s != "" {
		b.WriteString("\n## Summary\n\n" + s + "\n")
	}

	lastPath := ""
	for _, c := range sorted {
		if c.Path != lastPath {
			b.WriteString("\n## " + c.Path + "\n")
			lastPath = c.Path
		}
		b.WriteString(fmt.Sprintf("\n**%s** — `%s`\n\n", c.Heading(), c.anchor()))
		b.WriteString(strings.TrimRight(c.Body, "\n") + "\n")
	}
	return b.String()
}

// Write renders the review and writes it to <root>/.norn/review.md, returning
// the absolute path.
func Write(root, base, summary string, comments []Comment) (string, error) {
	dir := filepath.Join(root, Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "review.md")
	if err := os.WriteFile(path, []byte(Render(base, summary, comments)), 0o644); err != nil {
		return "", err
	}
	return path, nil
}
