package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sandbye/norn/internal/review"
)

// localDiffView builds a parsed, PR-less diff view sitting on a changed line.
func localDiffView(t *testing.T, root string) DiffView {
	t.Helper()
	dv := NewDiffView(root, "main", 1, []DiffFile{{Path: "a.go", Added: 1, Removed: 1}}, "")
	dv.width, dv.height = 120, 40
	m, _ := dv.Update(fileLoadedMsg{idx: 0, content: strings.Join([]string{
		"diff --git a/a.go b/a.go",
		"--- a/a.go",
		"+++ b/a.go",
		"@@ -1,3 +1,3 @@",
		" package main",
		"-const x = 1",
		"+const x = 2",
		"",
	}, "\n")})
	dv = m.(DiffView)
	dv.mode = modeFile
	return dv
}

func key(s string) tea.KeyMsg {
	if len(s) == 1 {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEscape}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "ctrl+s":
		return tea.KeyMsg{Type: tea.KeyCtrlS}
	case "ctrl+b":
		return tea.KeyMsg{Type: tea.KeyCtrlB}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func send(dv DiffView, keys ...string) DiffView {
	for _, k := range keys {
		m, _ := dv.Update(key(k))
		dv = m.(DiffView)
	}
	return dv
}

// TestLocalCommentFlow covers the no-PR path: c opens the label picker, a label
// key moves to the body, ctrl+s buffers a conventional comment.
func TestLocalCommentFlow(t *testing.T) {
	dv := localDiffView(t, t.TempDir())

	dv = send(dv, "c")
	if dv.mode != modeComment || !dv.labelPick {
		t.Fatalf("mode = %v labelPick = %v, want comment + picker", dv.mode, dv.labelPick)
	}
	if v := dv.View(); !strings.Contains(v, "i issue") {
		t.Fatalf("picker not rendered:\n%s", v)
	}

	// b toggles the blocking decoration, i picks "issue" and drops into the body.
	dv = send(dv, "b", "i")
	if dv.labelPick || dv.commentLabel != review.LabelIssue || !dv.commentBlocking {
		t.Fatalf("after picking: pick=%v label=%q blocking=%v",
			dv.labelPick, dv.commentLabel, dv.commentBlocking)
	}

	dv.commentArea.SetValue("off by one")
	dv = send(dv, "ctrl+s")
	if len(dv.pending) != 1 {
		t.Fatalf("pending = %d, want 1", len(dv.pending))
	}
	got := dv.pending[0]
	if got.Label != review.LabelIssue || !got.Blocking || got.Body != "off by one" {
		t.Fatalf("pending comment = %+v", got)
	}
	if want := "**issue (blocking):** off by one"; got.renderedBody() != want {
		t.Fatalf("renderedBody = %q, want %q", got.renderedBody(), want)
	}
	if dv.mode != modeFile {
		t.Fatalf("mode = %v after save, want modeFile", dv.mode)
	}
}

// TestLocalCommentBodyKeys covers tab (cycle label) and ctrl+b (blocking) from
// inside the body, and esc cancelling the picker.
func TestLocalCommentBodyKeys(t *testing.T) {
	dv := send(localDiffView(t, t.TempDir()), "c", "n")
	if dv.commentLabel != review.LabelNitpick {
		t.Fatalf("label = %q, want nitpick", dv.commentLabel)
	}
	dv = send(dv, "tab")
	if dv.commentLabel != review.LabelQuestion {
		t.Fatalf("label after tab = %q, want question", dv.commentLabel)
	}
	dv = send(dv, "ctrl+b")
	if !dv.commentBlocking {
		t.Fatal("ctrl+b did not set blocking")
	}

	dv = send(dv, "esc")
	if dv.mode != modeFile {
		t.Fatalf("mode = %v after esc, want modeFile", dv.mode)
	}
	if len(dv.pending) != 0 {
		t.Fatalf("esc buffered a comment: %+v", dv.pending)
	}
}

// TestLocalReviewWritesFileAndHandsOff drives R → ctrl+s → enter and checks the
// review lands on disk and the handoff flag is set for the caller.
func TestLocalReviewWritesFileAndHandsOff(t *testing.T) {
	root := t.TempDir()
	dv := send(localDiffView(t, root), "c", "s")
	dv.commentArea.SetValue("extract this")
	dv = send(dv, "ctrl+s", "R")
	if dv.mode != modeReview {
		t.Fatalf("mode = %v, want modeReview", dv.mode)
	}
	if v := dv.View(); strings.Contains(v, "REQUEST_CHANGES") {
		t.Fatalf("event picker shown for a local review:\n%s", v)
	}

	dv.reviewArea.SetValue("looks close")
	m, cmd := dv.Update(key("ctrl+s"))
	dv = m.(DiffView)
	if !dv.submitting || cmd == nil {
		t.Fatalf("submitting = %v, cmd = %v", dv.submitting, cmd)
	}
	// The batch runs the spinner tick and the write; run the write directly.
	msg := writeReviewCmd(root, dv.baseLabel(), "looks close", dv.pending)()
	rsm, ok := msg.(reviewSubmittedMsg)
	if !ok || rsm.err != nil {
		t.Fatalf("write produced %T (%v)", msg, msg)
	}
	m, _ = dv.Update(rsm)
	dv = m.(DiffView)
	if dv.mode != modeHandoff {
		t.Fatalf("mode = %v, want modeHandoff", dv.mode)
	}

	data, err := os.ReadFile(filepath.Join(root, review.File))
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, want := range []string{"**suggestion** — `a.go:", "looks close", "diffed against main"} {
		if !strings.Contains(body, want) {
			t.Errorf("review missing %q:\n%s", want, body)
		}
	}

	// enter hands off and quits; esc would keep us in the diff.
	m, quitCmd := dv.Update(key("enter"))
	after := m.(DiffView)
	if !after.Handoff() || quitCmd == nil {
		t.Fatalf("handoff = %v, cmd = %v", after.Handoff(), quitCmd)
	}
	if after.ReviewPath() == "" {
		t.Fatal("ReviewPath empty after write")
	}

	m, _ = dv.Update(key("esc"))
	stayed := m.(DiffView)
	if stayed.Handoff() || stayed.mode != modeFile || len(stayed.pending) != 0 {
		t.Fatalf("esc: handoff=%v mode=%v pending=%d",
			stayed.Handoff(), stayed.mode, len(stayed.pending))
	}
}

// TestLocalReviewNeedsContent guards the empty submit.
func TestLocalReviewNeedsContent(t *testing.T) {
	dv := send(localDiffView(t, t.TempDir()), "R")
	dv = send(dv, "ctrl+s")
	if dv.submitting {
		t.Fatal("submitted an empty local review")
	}
	if !strings.Contains(dv.statusMsg, "nothing to write") {
		t.Fatalf("statusMsg = %q", dv.statusMsg)
	}
}
