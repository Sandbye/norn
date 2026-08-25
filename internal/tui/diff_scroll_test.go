package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// diffViewWith builds a file-mode view of one synthetic file, sized to the
// terminal, with the diff already parsed.
func diffViewWith(t *testing.T, width, height int, body string) DiffView {
	t.Helper()
	files := []DiffFile{{Path: "main.go", Added: 40, Removed: 2}}
	dv := NewDiffView(t.TempDir(), "HEAD", 0, files, "").WithWorkingTree()
	m, _ := dv.Update(tea.WindowSizeMsg{Width: width, Height: height})
	dv = m.(DiffView)
	m, _ = dv.Update(fileLoadedMsg{idx: 0, content: body})
	return m.(DiffView)
}

// synthDiff builds a unified diff of n added lines, each `width` chars wide so
// the caller controls whether rows wrap.
func synthDiff(n, lineWidth int) string {
	var b strings.Builder
	b.WriteString("diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n")
	b.WriteString(fmt.Sprintf("@@ -1,1 +1,%d @@\n", n))
	for i := range n {
		b.WriteString("+" + fmt.Sprintf("%04d ", i) + strings.Repeat("x", lineWidth) + "\n")
	}
	return b.String()
}

// The rendered file view must fit the terminal. One line too many and the
// terminal scrolls the header off; the view then looks frozen and the top of
// the file is unreachable.
func TestFileViewFitsTerminalHeight(t *testing.T) {
	for _, tc := range []struct {
		name       string
		w, h, cols int
	}{
		{"short lines", 120, 30, 40},
		{"wrapping lines", 100, 30, 400},
		{"small terminal", 80, 14, 200},
		{"tall terminal", 140, 60, 60},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dv := diffViewWith(t, tc.w, tc.h, synthDiff(200, tc.cols))
			got := strings.Count(dv.renderFile(), "\n")
			if got > tc.h {
				t.Errorf("rendered %d lines into a %d-line terminal (overflow by %d)", got, tc.h, got-tc.h)
			}
		})
	}
}

// Moving the cursor must move the window. With wrapped rows the viewport holds
// far fewer logical rows than its height in lines, so a scroll calculation done
// in logical rows leaves the cursor rendered nowhere.
func TestFileViewCursorStaysVisible(t *testing.T) {
	dv := diffViewWith(t, 100, 30, synthDiff(200, 400)) // every row wraps ~4x
	for i := range 40 {
		m, _ := dv.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		dv = m.(DiffView)
		if !strings.Contains(dv.renderFile(), fmt.Sprintf("%04d", dv.parsed[dv.fileCursor].newNum-1)) {
			t.Fatalf("after %d j presses the cursor row (%d) is outside the rendered window (scroll=%d)",
				i+1, dv.fileCursor, dv.fileScroll)
		}
	}
}

// `g` returns to the top of the file, and the first row must actually render.
func TestFileViewGoToTop(t *testing.T) {
	dv := diffViewWith(t, 100, 30, synthDiff(200, 400))
	m, _ := dv.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	dv = m.(DiffView)
	m, _ = dv.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	dv = m.(DiffView)
	if dv.fileScroll != 0 {
		t.Fatalf("fileScroll = %d after g, want 0", dv.fileScroll)
	}
	if !strings.Contains(dv.renderFile(), "0000") {
		t.Error("first line of the file not rendered after g")
	}
}

// manyFiles builds a list-mode view with n files.
func manyFiles(t *testing.T, n, width, height int) DiffView {
	t.Helper()
	files := make([]DiffFile, n)
	for i := range files {
		files[i] = DiffFile{Path: fmt.Sprintf("pkg/file%03d.go", i), Added: i, Removed: 1}
	}
	dv := NewDiffView(t.TempDir(), "origin/main", 3, files, "")
	m, _ := dv.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return m.(DiffView)
}

// The file list must fit the terminal too. It used to print every file, so a
// large diff scrolled the top of the list off and the first file was
// unreachable.
func TestFileListFitsTerminalHeight(t *testing.T) {
	for _, n := range []int{5, 40, 200} {
		dv := manyFiles(t, n, 120, 30)
		if got := strings.Count(dv.renderList(), "\n"); got > 30 {
			t.Errorf("%d files: rendered %d lines into a 30-line terminal", n, got)
		}
	}
}

// The window follows the cursor in both directions, so the first and last file
// are both reachable.
func TestFileListWindowFollowsCursor(t *testing.T) {
	dv := manyFiles(t, 200, 120, 30)
	if !strings.Contains(dv.renderList(), "file000.go") {
		t.Error("first file not shown with the cursor at the top")
	}
	for range 199 {
		m, _ := dv.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		dv = m.(DiffView)
	}
	out := dv.renderList()
	if !strings.Contains(out, "file199.go") {
		t.Error("last file not shown with the cursor at the bottom")
	}
	if strings.Count(out, "\n") > 30 {
		t.Errorf("overflowed at the bottom of the list: %d lines", strings.Count(out, "\n"))
	}
}
