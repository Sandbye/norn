package task

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestOriginNWO(t *testing.T) {
	cases := map[string]string{
		"git@github.com:Sandbye/norn.git":          "Sandbye/norn",
		"https://github.com/Sandbye/norn.git":      "Sandbye/norn",
		"https://github.com/Sandbye/norn":          "Sandbye/norn",
		"ssh://git@github.com/Sandbye/norn.git":    "Sandbye/norn",
		"https://user@github.com/Sandbye/norn.git": "Sandbye/norn",
		"/Users/sandbye/Documents/GitHub/norn":     "", // local clone — no owner
		"../sibling-repo":                          "",
	}
	for url, want := range cases {
		dir := t.TempDir()
		if out, err := exec.Command("git", "init", "-q", "--bare", filepath.Join(dir, "r.git")).CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v: %s", err, out)
		}
		repo := filepath.Join(dir, "r.git")
		if out, err := exec.Command("git", "-C", repo, "remote", "add", "origin", url).CombinedOutput(); err != nil {
			t.Fatalf("remote add: %v: %s", err, out)
		}
		if got := originNWO(repo); got != want {
			t.Errorf("originNWO(%q) = %q, want %q", url, got, want)
		}
	}
}
