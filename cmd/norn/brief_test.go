package main

import "testing"

func TestParseBriefFlags(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want briefFlags
	}{
		{"issue", []string{"--repo", "/m/skuld.git", "--issue", "7"},
			briefFlags{repo: "/m/skuld.git", issue: 7, kind: "task"}},
		{"equals form", []string{"--repo=/m/skuld.git", "--issue=#7"},
			briefFlags{repo: "/m/skuld.git", issue: 7, kind: "task"}},
		{"hint only", []string{"--hint", "fix the payout rounding"},
			briefFlags{hint: "fix the payout rounding", kind: "task"}},
		{"type + base + template", []string{"--hint", "x", "--type", "chore", "--base", "develop", "-t", "task"},
			briefFlags{hint: "x", typ: "chore", base: "develop", template: "task", kind: "task"}},
	}
	for _, c := range cases {
		got, err := parseBriefFlags(c.args)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestParseBriefFlagsErrors(t *testing.T) {
	bad := [][]string{
		{},                                // no issue, no hint
		{"--repo", "/m/skuld.git"},        // ditto
		{"--issue", "zero"},               // not a number
		{"--issue", "0"},                  // not positive
		{"--issue"},                       // missing value
		{"--hint", "x", "--type", "feat"}, // not a Conventional Branch prefix
		{"--hint", "x", "--worktree"},     // unknown flag: never silently ignored
	}
	for _, args := range bad {
		if _, err := parseBriefFlags(args); err == nil {
			t.Errorf("parseBriefFlags(%q) = nil error, want one", args)
		}
	}
}
