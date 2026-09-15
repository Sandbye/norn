// Package paths resolves norn's config, state and cache directories, and holds
// the one rule for reading an install that still uses the old "work" namespace.
//
// The migration is read-only: norn reads a legacy directory where it finds one
// and never moves, copies or deletes it. Relocating a user's config is their
// call, so Notice reports the exact move instead of performing it.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// legacy is the pre-rename namespace. norn was called work until v0.2.
const legacy = "work"
const current = "norn"

var (
	mu   sync.Mutex
	seen = map[string]string{} // legacy dir in use -> its norn equivalent
)

// Config is ~/.config/norn, or ~/.config/work when only that exists.
func Config() string { return resolve(filepath.Join(home(), ".config")) }

// Cache is ~/.cache/norn, or ~/.cache/work when only that exists.
func Cache() string { return resolve(filepath.Join(home(), ".cache")) }

// State is ~/.local/state/norn, or the legacy dir when only that exists.
// Honors XDG_STATE_HOME, which the store has always respected.
func State() string {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return resolve(x)
	}
	return resolve(filepath.Join(home(), ".local", "state"))
}

// CacheAll lists every cache dir that may be in use: the resolved one, plus the
// legacy one when it still exists and is not already it. Only the cd-target
// handshake needs this, because a running shell holds a wrapper generated
// before the rename.
func CacheAll() []string {
	cur := Cache()
	dirs := []string{cur}
	old := filepath.Join(home(), ".cache", legacy)
	if old != cur {
		if _, err := os.Stat(old); err == nil {
			dirs = append(dirs, old)
		}
	}
	return dirs
}

// Projects is the per-project config dir inside Config.
func Projects() string { return filepath.Join(Config(), "projects") }

// Templates is the user template dir inside Config.
func Templates() string { return filepath.Join(Config(), "templates") }

// RepoConfigNames are the per-repo config filenames, preferred first. The old
// name still loads so a repo that committed one keeps working for everyone on
// the team, not just whoever renamed it.
var RepoConfigNames = []string{".norn.yaml", ".work.yaml"}

// RepoConfig returns the per-repo config path to read: the first name that
// exists, else the preferred name (what a writer should create).
func RepoConfig(repoRoot string) string {
	for _, n := range RepoConfigNames {
		p := filepath.Join(repoRoot, n)
		if _, err := os.Stat(p); err == nil {
			if n != RepoConfigNames[0] {
				note(p, filepath.Join(repoRoot, RepoConfigNames[0]))
			}
			return p
		}
	}
	return filepath.Join(repoRoot, RepoConfigNames[0])
}

// resolve picks <base>/norn, falling back to <base>/work when that is the only
// one present. A fresh install therefore never creates the legacy name, and an
// existing one is never stranded.
func resolve(base string) string {
	cur := filepath.Join(base, current)
	if _, err := os.Stat(cur); err == nil {
		return cur
	}
	old := filepath.Join(base, legacy)
	if _, err := os.Stat(old); err == nil {
		note(old, cur)
		return old
	}
	return cur
}

func note(old, cur string) {
	mu.Lock()
	seen[old] = cur
	mu.Unlock()
}

// Notice describes every legacy path this process read, with the move that
// retires it. Empty when nothing legacy was touched.
func Notice() string {
	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 {
		return ""
	}
	olds := make([]string, 0, len(seen))
	for o := range seen {
		olds = append(olds, o)
	}
	sort.Strings(olds)
	var b strings.Builder
	b.WriteString("norn still reads the old \"work\" paths. To retire them:\n")
	for _, o := range olds {
		fmt.Fprintf(&b, "  mv %s %s\n", tilde(o), tilde(seen[o]))
	}
	return b.String()
}

func tilde(p string) string {
	if h := home(); h != "" && strings.HasPrefix(p, h+string(os.PathSeparator)) {
		return "~" + p[len(h):]
	}
	return p
}

func home() string {
	h, _ := os.UserHomeDir()
	return h
}
