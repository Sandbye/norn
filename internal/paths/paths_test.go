package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// reset clears the notice state so each case starts clean.
func reset(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "")
	mu.Lock()
	seen = map[string]string{}
	mu.Unlock()
}

func TestFreshInstallUsesNorn(t *testing.T) {
	home := t.TempDir()
	reset(t, home)

	if got, want := Config(), filepath.Join(home, ".config", "norn"); got != want {
		t.Errorf("Config() = %s, want %s", got, want)
	}
	if got, want := Cache(), filepath.Join(home, ".cache", "norn"); got != want {
		t.Errorf("Cache() = %s, want %s", got, want)
	}
	if n := Notice(); n != "" {
		t.Errorf("fresh install produced a migration notice:\n%s", n)
	}
}

// The case the rename has to not break: an install that only has the old dirs.
func TestLegacyOnlyInstallKeepsReading(t *testing.T) {
	home := t.TempDir()
	reset(t, home)
	legacyCfg := filepath.Join(home, ".config", "work")
	if err := os.MkdirAll(legacyCfg, 0o755); err != nil {
		t.Fatal(err)
	}

	if got := Config(); got != legacyCfg {
		t.Fatalf("Config() = %s, want the legacy dir %s", got, legacyCfg)
	}
	// The notice is for a human to run, so it says ~/.config/work, not $TMPDIR.
	n := Notice()
	if !strings.Contains(n, "mv ~/.config/work ~/.config/norn") {
		t.Errorf("notice does not name the move:\n%s", n)
	}
}

// Once the new dir exists it wins, even with the old one still on disk, so a
// half-finished manual move can't leave norn reading two places at once.
func TestNornDirWinsOverLegacy(t *testing.T) {
	home := t.TempDir()
	reset(t, home)
	for _, d := range []string{"work", "norn"} {
		if err := os.MkdirAll(filepath.Join(home, ".config", d), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if got, want := Config(), filepath.Join(home, ".config", "norn"); got != want {
		t.Errorf("Config() = %s, want %s", got, want)
	}
	if n := Notice(); n != "" {
		t.Errorf("both dirs present should not warn:\n%s", n)
	}
}

func TestRepoConfigPrefersNornButReadsWork(t *testing.T) {
	home := t.TempDir()

	reset(t, home)
	repo := t.TempDir()
	if got, want := RepoConfig(repo), filepath.Join(repo, ".norn.yaml"); got != want {
		t.Errorf("empty repo: RepoConfig = %s, want the preferred name %s", got, want)
	}

	reset(t, home)
	old := filepath.Join(repo, ".work.yaml")
	if err := os.WriteFile(old, []byte("theme: nord\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := RepoConfig(repo); got != old {
		t.Errorf("legacy only: RepoConfig = %s, want %s", got, old)
	}
	if n := Notice(); !strings.Contains(n, ".work.yaml") {
		t.Errorf("legacy repo config did not warn:\n%s", n)
	}

	reset(t, home)
	newp := filepath.Join(repo, ".norn.yaml")
	if err := os.WriteFile(newp, []byte("theme: frog\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := RepoConfig(repo); got != newp {
		t.Errorf("both present: RepoConfig = %s, want %s", got, newp)
	}
}

// CacheAll is what keeps a shell started before the rename working: it still
// runs a wrapper reading the old dir, so the cd-target has to land in both.
func TestCacheAllCoversALegacyShell(t *testing.T) {
	home := t.TempDir()
	reset(t, home)
	legacy := filepath.Join(home, ".cache", "work")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".cache", "norn"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := CacheAll()
	if len(got) != 2 {
		t.Fatalf("CacheAll() = %v, want both dirs", got)
	}
	if got[0] != filepath.Join(home, ".cache", "norn") || got[1] != legacy {
		t.Errorf("CacheAll() = %v, want norn first then the legacy dir", got)
	}
}
