package tui

import (
	"sort"
	"strings"

	"github.com/sandbye/norn/internal/git"
)

// filterState is a shared `/`-to-search input used by the menu and clean views.
type filterState struct {
	active bool
	query  string
}

// handleKey processes a key for the filter and reports whether it consumed it.
// `/` activates; while active, printable chars + backspace edit the query and
// esc exits (clearing it). Navigation/selection keys are left for the caller.
func (f *filterState) handleKey(s string) bool {
	if !f.active {
		if s == "/" {
			f.active = true
			return true
		}
		return false
	}
	switch s {
	case "esc":
		f.active = false
		f.query = ""
		return true
	case "backspace", "ctrl+h":
		if len(f.query) > 0 {
			f.query = f.query[:len(f.query)-1]
		}
		return true
	default:
		if len(s) == 1 && s[0] >= 0x20 && s[0] < 0x7f {
			f.query += s
			return true
		}
	}
	return false
}

// rankWorktrees returns worktrees matching query, best-ranked first. Empty
// query returns the input order unchanged.
func rankWorktrees(wts []git.Worktree, query string) []git.Worktree {
	if query == "" {
		return wts
	}
	type scored struct {
		wt    git.Worktree
		score int
	}
	var hits []scored
	for _, wt := range wts {
		// Match against branch + commit message so either can find a row.
		s1, ok1 := fuzzyScore(query, wt.Branch)
		s2, ok2 := fuzzyScore(query, wt.CommitMsg)
		if !ok1 && !ok2 {
			continue
		}
		score := s1
		if s2 > score {
			score = s2
		}
		hits = append(hits, scored{wt, score})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	out := make([]git.Worktree, len(hits))
	for i, h := range hits {
		out[i] = h.wt
	}
	return out
}

// fuzzyScore reports whether query is a subsequence of target (case-insensitive)
// and a rank score — higher is better. Returns (0, false) on no match.
//
// Scoring favours, in rough order: contiguous runs, matches at word boundaries
// (start, or after / - _ space .), and earlier matches. Good enough for ranking
// branch names like `feature/#86c.../foo-bar` without pulling in a dep.
func fuzzyScore(query, target string) (int, bool) {
	if query == "" {
		return 1, true
	}
	q := strings.ToLower(query)
	t := strings.ToLower(target)

	score := 0
	ti := 0
	prevMatch := -1 // index of previous matched rune in target
	for qi := 0; qi < len(q); qi++ {
		c := q[qi]
		found := -1
		for ; ti < len(t); ti++ {
			if t[ti] == c {
				found = ti
				break
			}
		}
		if found < 0 {
			return 0, false
		}
		score += 1 // base point per matched char
		if qi == 0 {
			// Prefer matches that start earlier in the string.
			score -= found / 10
		} else if found == prevMatch+1 {
			// Contiguous run — the dominant signal, so a tight match always
			// beats one scattered across word boundaries.
			score += 10
		} else {
			// Gap penalty grows with the distance skipped.
			score -= found - prevMatch - 1
		}
		// Small word-boundary nudge (start, or after / - _ space . #).
		if found == 0 || isBoundary(t[found-1]) {
			score += 2
		}
		prevMatch = found
		ti = found + 1
	}
	return score, true
}

func isBoundary(b byte) bool {
	switch b {
	case '/', '-', '_', ' ', '.', '#':
		return true
	}
	return false
}
