// Package state persists per-session metadata for the work CLI.
//
// One JSON file at ~/.local/state/norn/sessions.json holds every active and
// recent worktree session. Sized for ~10 concurrent sessions; if it ever grows,
// migrate to sqlite via modernc.org/sqlite. Atomic writes via tmp+rename.
package state

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/sandbye/norn/internal/paths"
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
	Title          string    `json:"title,omitempty"` // human task title, sourced from .worktree.md
	ClickUpID      string    `json:"clickup_id,omitempty"`
	PRNumber       int       `json:"pr_number,omitempty"`
	Status         string    `json:"status"` // active | merged | abandoned
	StartedAt      time.Time `json:"started_at"`
	LastActivityAt time.Time `json:"last_activity_at"`
	Blockers       []string  `json:"blockers,omitempty"`

	// TaskID ties this worktree to a Task in the same store; Role is the part it
	// plays in it ("logic", "assets", …). Both empty for a standalone worktree,
	// and omitempty so a store written before tasks existed loads unchanged.
	TaskID string `json:"task_id,omitempty"`
	Role   string `json:"role,omitempty"`
}

// Task is one piece of work split across several worktrees. Every Session
// carrying its ID is one role thread of it. Trunk is the integrating role's
// branch: role branches merge into it, and only it opens a PR.
type Task struct {
	ID        string    `json:"id"`
	Repo      string    `json:"repo"`
	Goal      string    `json:"goal,omitempty"`
	Trunk     string    `json:"trunk"`
	CreatedAt time.Time `json:"created_at"`
}

// Store is the on-disk session list, loaded into memory.
type Store struct {
	Sessions []Session `json:"sessions"`
	Tasks    []Task    `json:"tasks,omitempty"`

	// repaired records that Load rewrote paths or dropped duplicates, so
	// Mutate persists the repair even when its fn changes nothing.
	repaired bool
}

// Path returns the canonical store path. Honors XDG_STATE_HOME.
func Path() string {
	return filepath.Join(paths.State(), "sessions.json")
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
	s.canonicalizePaths()
	return &s, nil
}

// canonicalizePaths rewrites every stored path through paths.Canon and collapses
// rows that then share one, keeping the most recently active. Repairs stores
// written before paths were canonical; idempotent afterwards.
func (s *Store) canonicalizePaths() {
	at := map[string]int{}
	out := s.Sessions[:0]
	for _, sess := range s.Sessions {
		canon := paths.Canon(sess.Path)
		if canon != sess.Path {
			sess.Path = canon
			s.repaired = true
		}
		i, dup := at[canon]
		if !dup {
			at[canon] = len(out)
			out = append(out, sess)
			continue
		}
		s.repaired = true
		if sess.LastActivityAt.After(out[i].LastActivityAt) {
			out[i] = sess
		}
	}
	s.Sessions = out
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
	sess.Path = paths.Canon(sess.Path)
	if existing := s.Find(sess.ID); existing != nil {
		// Preserve fields the caller didn't set.
		if sess.Title == "" {
			sess.Title = existing.Title
		}
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
		if sess.TaskID == "" {
			sess.TaskID = existing.TaskID
			sess.Role = existing.Role
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
	path = paths.Canon(path)
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
	sess.Path = paths.Canon(sess.Path)
	if existing := s.FindByPath(sess.Path); existing != nil {
		if sess.Title == "" {
			sess.Title = existing.Title
		}
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
		if sess.TaskID == "" {
			sess.TaskID = existing.TaskID
			sess.Role = existing.Role
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
		canon := paths.Canon(sess.Path)
		if seen[canon] {
			removed++
			continue
		}
		seen[canon] = true
		sess.Path = canon
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

// NewTaskID mints an opaque task id. Opaque, not derived from repo+trunk: the
// trunk branch can be renamed, and a derived id would dangle on every session
// row pointing at it.
func NewTaskID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// FindTask returns the task with the matching id, or nil.
func (s *Store) FindTask(id string) *Task {
	for i := range s.Tasks {
		if s.Tasks[i].ID == id {
			return &s.Tasks[i]
		}
	}
	return nil
}

// FindTaskByTrunk returns the task owning the given trunk branch in the given
// repo, or nil. This is the lookup for "does a task already exist for this
// branch", since the id is opaque.
func (s *Store) FindTaskByTrunk(repo, trunk string) *Task {
	for i := range s.Tasks {
		if s.Tasks[i].Repo == repo && s.Tasks[i].Trunk == trunk {
			return &s.Tasks[i]
		}
	}
	return nil
}

// UpsertTask inserts or updates a task by id, minting one when the caller left
// it empty. Fields the caller didn't set are preserved, same contract as
// UpsertByPath.
func (s *Store) UpsertTask(t Task) *Task {
	if t.ID == "" {
		t.ID = NewTaskID()
	}
	if existing := s.FindTask(t.ID); existing != nil {
		if t.Goal == "" {
			t.Goal = existing.Goal
		}
		if t.Trunk == "" {
			t.Trunk = existing.Trunk
		}
		if t.Repo == "" {
			t.Repo = existing.Repo
		}
		if t.CreatedAt.IsZero() {
			t.CreatedAt = existing.CreatedAt
		}
		*existing = t
		return existing
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now()
	}
	s.Tasks = append(s.Tasks, t)
	return &s.Tasks[len(s.Tasks)-1]
}

// SessionsForTask returns the rows belonging to a task, in store order.
func (s *Store) SessionsForTask(id string) []Session {
	if id == "" {
		return nil
	}
	var out []Session
	for _, sess := range s.Sessions {
		if sess.TaskID == id {
			out = append(out, sess)
		}
	}
	return out
}

// PruneTasks drops tasks no session points at any more, and clears a TaskID
// that points at no task. Both halves matter: the dashboard prunes rows whose
// worktree is gone, which would otherwise leave a task header over nothing.
// Returns the number of changes made, so a Mutate caller can report dirty.
func (s *Store) PruneTasks() int {
	live := map[string]bool{}
	for _, sess := range s.Sessions {
		if sess.TaskID != "" {
			live[sess.TaskID] = true
		}
	}
	known := map[string]bool{}
	out := s.Tasks[:0]
	removed := 0
	for _, t := range s.Tasks {
		if !live[t.ID] {
			removed++
			continue
		}
		known[t.ID] = true
		out = append(out, t)
	}
	s.Tasks = out
	for i := range s.Sessions {
		if id := s.Sessions[i].TaskID; id != "" && !known[id] {
			s.Sessions[i].TaskID = ""
			s.Sessions[i].Role = ""
			removed++
		}
	}
	return removed
}
