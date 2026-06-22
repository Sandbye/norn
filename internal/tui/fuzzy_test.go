package tui

import "testing"

func TestFuzzyScore(t *testing.T) {
	// subsequence matches
	matches := []struct{ q, target string }{
		{"", "anything"},
		{"foo", "feature/#86c/foo-bar"},
		{"dsb", "chore/dashboard-rewrite"},
	}
	for _, c := range matches {
		if _, ok := fuzzyScore(c.q, c.target); !ok {
			t.Errorf("fuzzyScore(%q, %q) = no match, want match", c.q, c.target)
		}
	}

	// non-matches
	if _, ok := fuzzyScore("xyz", "feature/foo"); ok {
		t.Error("fuzzyScore(xyz, feature/foo) matched, want no match")
	}

	// ranking: boundary/contiguous match outranks scattered
	hi, _ := fuzzyScore("foo", "feat/foo-bar")
	lo, _ := fuzzyScore("foo", "fix/old-orders")
	if hi <= lo {
		t.Errorf("expected 'foo' to rank feat/foo-bar (%d) above fix/old-orders (%d)", hi, lo)
	}
}
