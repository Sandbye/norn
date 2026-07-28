// Package state persists per-session metadata for the work CLI.
//
// One JSON file at ~/.local/state/work/sessions.json holds every active and
// recent worktree session. Sized for ~10 concurrent sessions; if it ever grows,
// migrate to sqlite via modernc.org/sqlite. Atomic writes via tmp+rename.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const StatusActive = "active"
const StatusMerged = "merged"
const StatusAbandoned = "abandoned"

// Session is one worktree-level row.
type Session struct {
	ID             string    `json:"id"`              // <repo>:<branch>
	Repo           string    `json:"repo"`            // repo basename
	Branch         string    `json:"branch"`          // full branch name
	Kind           string    `json:"kind"`            // task | review
	Path           string    `json:"path"`            // worktree absolute path
	ClickUpID      string    `json:"clickup_id,omitempty"`
	PRNumber       int       `json:"pr_number,omitempty"`
	Status         string    `json:"status"`          // active | merged | abandoned
	StartedAt      time.Time `json:"started_at"`
	LastActivityAt time.Time `json:"last_activity_at"`
	Blockers       []string  `json:"blockers,omitempty"`
}

// Store is the on-disk session list, loaded into memory.
type Store struct {
	Sessions []Session `json:"sessions"`
}

// Path returns the canonical store path. Honors XDG_STATE_HOME.
func Path() string {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "work", "sessions.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "work", "sessions.json")
}

// Load reads the store. Returns an empty store if the file doesn't exist.
func Load() (*Store, error) {
	p := Path()
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return &Store{}, nil
		}
		return nil, fmt.Errorf("load %s: %w", p, err)
	}
	var s Store
	if len(data) == 0 {
		return &s, nil
	}
	if err := json.Unmarshal(data, &s); err != nil {
		// Don't brick on a corrupt file: move it aside and start fresh. The
		// dashboard reconciles against live worktrees, so the rows rebuild.
		_ = os.Rename(p, p+".corrupt")
		return &Store{}, nil
	}
	return &s, nil
}

// Save writes the store atomically via a unique temp file + rename. A unique
// temp (not a fixed "<p>.tmp") matters: multiple norn processes write this file
// concurrently (the TUI plus per-tool-call activity-tick hooks), and a shared
// temp path lets their writes interleave and corrupt it. Each writer getting
// its own temp + an atomic rename means a reader always sees a whole document
// (last writer wins; no interleaving).
func (s *Store) Save() error {
	p := Path()
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".sessions-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, p); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// Find returns the session with the matching id, or nil.
func (s *Store) Find(id string) *Session {
	for i := range s.Sessions {
		if s.Sessions[i].ID == id {
			return &s.Sessions[i]
		}
	}
	return nil
}

// Upsert inserts or updates a session by id. Returns the merged session.
func (s *Store) Upsert(sess Session) *Session {
	if existing := s.Find(sess.ID); existing != nil {
		// Preserve fields the caller didn't set.
		if sess.ClickUpID == "" {
			sess.ClickUpID = existing.ClickUpID
		}
		if sess.PRNumber == 0 {
			sess.PRNumber = existing.PRNumber
		}
		if sess.Status == "" {
			sess.Status = existing.Status
		}
		if sess.StartedAt.IsZero() {
			sess.StartedAt = existing.StartedAt
		}
		if len(sess.Blockers) == 0 {
			sess.Blockers = existing.Blockers
		}
		*existing = sess
		return existing
	}
	if sess.Status == "" {
		sess.Status = StatusActive
	}
	if sess.StartedAt.IsZero() {
		sess.StartedAt = time.Now()
	}
	if sess.LastActivityAt.IsZero() {
		sess.LastActivityAt = sess.StartedAt
	}
	s.Sessions = append(s.Sessions, sess)
	return &s.Sessions[len(s.Sessions)-1]
}

// FindByPath returns the session at the given worktree path, or nil. Worktree
// path is the stable identity of a thread; branch may change under it.
func (s *Store) FindByPath(path string) *Session {
	for i := range s.Sessions {
		if s.Sessions[i].Path == path {
			return &s.Sessions[i]
		}
	}
	return nil
}

// UpsertByPath dedups on worktree path: one row per worktree. If a row for the
// path exists it's updated in place (branch may have changed); otherwise the
// session is appended. Prevents branch switches from spawning duplicate rows.
func (s *Store) UpsertByPath(sess Session) *Session {
	if existing := s.FindByPath(sess.Path); existing != nil {
		if sess.ClickUpID == "" {
			sess.ClickUpID = existing.ClickUpID
		}
		if sess.PRNumber == 0 {
			sess.PRNumber = existing.PRNumber
		}
		if sess.Status == "" {
			sess.Status = existing.Status
		}
		if sess.StartedAt.IsZero() {
			sess.StartedAt = existing.StartedAt
		}
		if len(sess.Blockers) == 0 {
			sess.Blockers = existing.Blockers
		}
		*existing = sess
		return existing
	}
	if sess.Status == "" {
		sess.Status = StatusActive
	}
	if sess.StartedAt.IsZero() {
		sess.StartedAt = time.Now()
	}
	if sess.LastActivityAt.IsZero() {
		sess.LastActivityAt = sess.StartedAt
	}
	s.Sessions = append(s.Sessions, sess)
	return &s.Sessions[len(s.Sessions)-1]
}

// Prune keeps only sessions for which keep returns true. Returns removed count.
func (s *Store) Prune(keep func(Session) bool) int {
	out := s.Sessions[:0]
	removed := 0
	for _, sess := range s.Sessions {
		if keep(sess) {
			out = append(out, sess)
		} else {
			removed++
		}
	}
	s.Sessions = out
	return removed
}

// DedupeByPath collapses rows sharing a worktree path down to the first seen.
// Call SortByActivity first so the newest row survives.
func (s *Store) DedupeByPath() int {
	seen := map[string]bool{}
	out := s.Sessions[:0]
	removed := 0
	for _, sess := range s.Sessions {
		if seen[sess.Path] {
			removed++
			continue
		}
		seen[sess.Path] = true
		out = append(out, sess)
	}
	s.Sessions = out
	return removed
}

// Tick bumps last_activity_at for the session with the given id.
// Returns false if no session matches (caller decides whether to create).
func (s *Store) Tick(id string) bool {
	if sess := s.Find(id); sess != nil {
		sess.LastActivityAt = time.Now()
		return true
	}
	return false
}

// Remove drops a session by id. No-op if not present.
func (s *Store) Remove(id string) {
	out := s.Sessions[:0]
	for _, sess := range s.Sessions {
		if sess.ID != id {
			out = append(out, sess)
		}
	}
	s.Sessions = out
}

// SortByActivity sorts in-place, most-recent activity first.
func (s *Store) SortByActivity() {
	sort.Slice(s.Sessions, func(i, j int) bool {
		return s.Sessions[i].LastActivityAt.After(s.Sessions[j].LastActivityAt)
	})
}

// MakeID builds the canonical session id from repo + branch.
func MakeID(repo, branch string) string {
	return repo + ":" + branch
}
