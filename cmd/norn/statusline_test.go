package main

import (
	"strings"
	"testing"
)

func pct(f float64) *float64 { return &f }

// strip removes the ANSI colour so assertions read on content, not escapes.
func strip(s string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, "\x1b[")
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		j := strings.Index(s[i:], "m")
		if j < 0 {
			return b.String()
		}
		s = s[i+j+1:]
	}
}

func TestStatuslineCard(t *testing.T) {
	const state = `task:    demo
goal:    make re-entry cheap
next:    wire the statusline into the resume hook
blocked: none
`
	cases := []struct {
		name     string
		branch   string
		stateMD  string
		ctx      *float64
		want     []string
		notWant  []string
		wantLine int
	}{
		{
			name: "next is the headline", branch: "fix/rounding", stateMD: state, ctx: pct(42.7),
			want:     []string{"fix/rounding", "ctx 43%", "→ wire the statusline into the resume hook"},
			wantLine: 2,
		},
		{
			name: "no state file still names the branch", branch: "fix/rounding", stateMD: "", ctx: pct(5),
			want: []string{"fix/rounding", "ctx 5%"}, wantLine: 1,
		},
		{
			name: "no context percentage yet", branch: "fix/rounding", stateMD: "", ctx: nil,
			want: []string{"fix/rounding"}, notWant: []string{"ctx"}, wantLine: 1,
		},
		{
			name:   "blocked gets its own line",
			branch: "fix/rounding",
			stateMD: `goal:    ship it
next:    ask about the key
blocked: waiting on Stripe test keys
`,
			want: []string{"→ ask about the key", "⨯ waiting on Stripe test keys"}, wantLine: 3,
		},
		{
			name:    "goal carries the card when next is absent",
			branch:  "fix/rounding",
			stateMD: "goal:    make re-entry cheap\n",
			want:    []string{"make re-entry cheap"}, notWant: []string{"→"}, wantLine: 2,
		},
		{
			// "blocked: none" is the contract's way of saying clear, so showing it
			// would put a red line on every healthy thread.
			name:    "blocked none is not a blocker",
			branch:  "fix/rounding",
			stateMD: "next:    keep going\nblocked: none\n",
			notWant: []string{"⨯"}, wantLine: 2,
		},
		{
			// Detached HEAD: CurrentBranch returns "", and the directory name is
			// the only label left.
			name: "detached falls back to the directory", branch: "", stateMD: "",
			want: []string{"rounding"}, wantLine: 1,
		},
	}

	for _, c := range cases {
		got := strip(statuslineCard("/tmp/wt/fix/rounding", c.branch, c.stateMD, c.ctx))
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: missing %q in:\n%s", c.name, w, got)
			}
		}
		for _, w := range c.notWant {
			if strings.Contains(got, w) {
				t.Errorf("%s: unexpected %q in:\n%s", c.name, w, got)
			}
		}
		if n := len(strings.Split(strings.TrimRight(got, "\n"), "\n")); n != c.wantLine {
			t.Errorf("%s: %d lines, want %d:\n%s", c.name, n, c.wantLine, got)
		}
	}
}

// The status bar is one row per line: a long next would otherwise wrap and
// push the prompt around on every render.
func TestStatuslineTruncatesLongText(t *testing.T) {
	long := strings.Repeat("wire the oauth callback ", 20)
	got := strip(statuslineCard("/tmp/wt/x", "feature/login", "next: "+long+"\n", nil))

	for _, line := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
		if n := len([]rune(line)); n > statuslineMax+4 {
			t.Errorf("line of %d runes exceeds the cap:\n%s", n, line)
		}
	}
	if !strings.Contains(got, "…") {
		t.Errorf("truncated text is not marked:\n%s", got)
	}
}

// Multi-byte text must not be cut mid-rune, which would print a replacement glyph.
func TestStatuslineTruncatesOnRunes(t *testing.T) {
	got := truncate(strings.Repeat("æøå", 200), 10)
	if n := len([]rune(got)); n != 10 {
		t.Errorf("truncate returned %d runes, want 10: %q", n, got)
	}
	if strings.Contains(got, "\uFFFD") {
		t.Errorf("truncate split a rune: %q", got)
	}
}
