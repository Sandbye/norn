package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestSaveConcurrentStaysValid reproduces the corruption that a fixed
// "<p>.tmp" caused: many processes writing at once interleaved into the shared
// temp and left invalid JSON (e.g. a trailing "}"). With a unique temp per
// write + atomic rename, the file is always a complete document.
func TestSaveConcurrentStaysValid(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := &Store{Sessions: []Session{{
				ID: fmt.Sprint(i), Repo: "r", Branch: "b", Path: "/p", Status: StatusActive,
			}}}
			for j := 0; j < 5; j++ {
				_ = s.Save()
			}
		}(i)
	}
	wg.Wait()

	// Read the raw file (not via Load, which self-heals) and require valid JSON.
	data, err := os.ReadFile(Path())
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	var chk Store
	if err := json.Unmarshal(data, &chk); err != nil {
		t.Fatalf("state file corrupt after concurrent saves: %v\n%s", err, data)
	}
}

// TestLoadCorruptSelfHeals: a corrupt file must not brick Load; it moves the
// bad file aside and returns an empty store (the dashboard rebuilds from live
// worktrees).
func TestLoadCorruptSelfHeals(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	p := Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"sessions":[]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load()
	if err != nil {
		t.Fatalf("Load should not error on corrupt file: %v", err)
	}
	if len(s.Sessions) != 0 {
		t.Errorf("expected empty store, got %d sessions", len(s.Sessions))
	}
	if _, err := os.Stat(p + ".corrupt"); err != nil {
		t.Errorf("corrupt file not moved aside: %v", err)
	}
}

// TestMutateConcurrentKeepsEveryRow is the lost-row case Save alone can't fix:
// each writer loads, appends its own row and saves, so without a lock spanning
// load-through-save the last writer's document is missing everyone else's rows.
func TestMutateConcurrentKeepsEveryRow(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	const writers = 25
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := Mutate(func(s *Store) bool {
				s.Sessions = append(s.Sessions, Session{
					ID: fmt.Sprint(i), Repo: "r", Branch: fmt.Sprint(i),
					Path: fmt.Sprintf("/p/%d", i), Status: StatusActive,
				})
				return true
			}); err != nil {
				t.Errorf("Mutate: %v", err)
			}
		}(i)
	}
	wg.Wait()

	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(s.Sessions) != writers {
		t.Fatalf("rows = %d, want %d — a concurrent write was lost", len(s.Sessions), writers)
	}
	seen := map[string]bool{}
	for _, sess := range s.Sessions {
		seen[sess.ID] = true
	}
	for i := 0; i < writers; i++ {
		if !seen[fmt.Sprint(i)] {
			t.Errorf("row %d missing", i)
		}
	}
}

// symlinkedWorktree returns (realPath, linkedPath) for the same directory: the
// shape of a default macOS install, where /tmp is a symlink to /private/tmp.
func symlinkedWorktree(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(real, "worktrees", "feature", "dupe-check")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(real, "link")
	if err := os.Symlink(filepath.Join(real, "worktrees"), link); err != nil {
		t.Fatal(err)
	}
	return wt, filepath.Join(link, "feature", "dupe-check")
}

// TestUpsertByPathResolvesSymlinks: the same worktree reached through a symlink
// is the same row. The path is stored once as built and once as read back from
// `git worktree list`, and those two spellings must not both become rows.
func TestUpsertByPathResolvesSymlinks(t *testing.T) {
	real, link := symlinkedWorktree(t)

	s := &Store{}
	s.UpsertByPath(Session{ID: "app:feature/dupe-check", Repo: "app", Branch: "feature/dupe-check", Path: real})
	s.UpsertByPath(Session{ID: "app:feature/dupe-check", Repo: "app", Branch: "feature/dupe-check", Path: link})

	if len(s.Sessions) != 1 {
		t.Fatalf("rows = %d, want 1: %+v", len(s.Sessions), s.Sessions)
	}
	if s.Sessions[0].Path != real {
		t.Errorf("stored path = %q, want the resolved %q", s.Sessions[0].Path, real)
	}
	if s.FindByPath(link) == nil {
		t.Error("FindByPath through the symlink found nothing")
	}
}

// TestLoadCollapsesSymlinkDuplicates: installs that already wrote both
// spellings self-heal on the next load, keeping the most recently active row.
func TestLoadCollapsesSymlinkDuplicates(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	real, link := symlinkedWorktree(t)

	old := time.Now().Add(-time.Hour)
	seed := &Store{Sessions: []Session{
		{ID: "app:b", Repo: "app", Branch: "b", Path: link, Status: StatusActive, StartedAt: old, LastActivityAt: old, Title: "from the symlinked row"},
		{ID: "app:b", Repo: "app", Branch: "b", Path: real, Status: StatusActive, StartedAt: old, LastActivityAt: time.Now()},
	}}
	if err := seed.Save(); err != nil {
		t.Fatal(err)
	}

	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(s.Sessions) != 1 {
		t.Fatalf("rows = %d, want 1: %+v", len(s.Sessions), s.Sessions)
	}
	if s.Sessions[0].Path != real {
		t.Errorf("surviving path = %q, want %q", s.Sessions[0].Path, real)
	}
	if s.Sessions[0].Title != "" {
		t.Errorf("kept the stale row: title = %q", s.Sessions[0].Title)
	}

	// The repair reaches disk on the next write, even one that changes nothing.
	if _, err := Mutate(func(*Store) bool { return false }); err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	data, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	var chk Store
	if err := json.Unmarshal(data, &chk); err != nil {
		t.Fatal(err)
	}
	if len(chk.Sessions) != 1 {
		t.Fatalf("on-disk rows = %d, want 1", len(chk.Sessions))
	}
}
