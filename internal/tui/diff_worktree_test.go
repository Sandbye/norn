package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestWorkingTreeDiff drives the working-tree DiffView against a real temp git
// repo: it must open straight into the file view and load the uncommitted diff.
func TestWorkingTreeDiff(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	f := filepath.Join(repo, "hello.txt")
	if err := os.WriteFile(f, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "init")
	// Uncommitted change.
	if err := os.WriteFile(f, []byte("one\ntwo CHANGED\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	files := []DiffFile{{Path: "hello.txt", Added: 2, Removed: 1}}
	dv := NewDiffView(repo, "HEAD", 0, files, "").WithWorkingTree()

	// Opens straight into the file view, not the list.
	if dv.mode != modeFile {
		t.Fatalf("mode = %v, want modeFile", dv.mode)
	}

	// Init loads the first file's diff.
	cmd := dv.Init()
	if cmd == nil {
		t.Fatal("Init returned nil cmd; expected a load")
	}
	msg := cmd()
	flm, ok := msg.(fileLoadedMsg)
	if !ok {
		t.Fatalf("Init cmd produced %T, want fileLoadedMsg", msg)
	}
	if !strings.Contains(flm.content, "CHANGED") {
		t.Fatalf("loaded diff missing the change:\n%s", flm.content)
	}

	// Feeding the msg back parses the diff and the view renders the change.
	m2, _ := dv.Update(flm)
	dv2 := m2.(DiffView)
	if len(dv2.parsed) == 0 {
		t.Fatal("parsed empty after fileLoadedMsg")
	}
	dv2.width, dv2.height = 120, 40
	if v := dv2.View(); !strings.Contains(v, "CHANGED") {
		t.Fatalf("View missing the change:\n%s", v)
	}
}
