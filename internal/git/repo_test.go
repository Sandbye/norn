package git

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRepoNameFromGitDir(t *testing.T) {
	cases := map[string]string{
		"/Users/x/GitHub/norn/.git":        "norn",
		"/Users/x/mirrors/skuld.git":       "skuld",
		"/Users/x/mirrors/skuld":           "skuld",
		"/Users/x/norn/.git/modules/theme": "theme",
	}
	for in, want := range cases {
		if got := RepoNameFromGitDir(in); got != want {
			t.Errorf("RepoNameFromGitDir(%q) = %q, want %q", in, got, want)
		}
	}
}

// A bare mirror is the case that matters for `norn brief`: no checkout, so
// --show-toplevel fails and the git dir itself is the repo root.
func TestRepoRootAtBare(t *testing.T) {
	dir := t.TempDir()
	bare := filepath.Join(dir, "skuld.git")
	if out, err := exec.Command("git", "init", "--bare", "-q", bare).CombinedOutput(); err != nil {
		t.Skipf("git init --bare unavailable: %v: %s", err, out)
	}

	root, err := RepoRootAt(bare)
	if err != nil {
		t.Fatalf("RepoRootAt(%q): %v", bare, err)
	}
	if filepath.Base(root) != "skuld.git" {
		t.Errorf("root = %q, want it to end in skuld.git", root)
	}
	if got := OriginRepoName(bare); got != "skuld" {
		t.Errorf("OriginRepoName = %q, want %q", got, "skuld")
	}
}

func TestRepoRootAtNotARepo(t *testing.T) {
	if _, err := RepoRootAt(t.TempDir()); err == nil {
		t.Error("RepoRootAt on a non-repo dir = nil error, want one")
	}
}
