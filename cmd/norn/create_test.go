package main

import (
	"strings"
	"testing"
)

func TestExtractCreateFlags(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want createFlags
	}{
		{"hint only", []string{"add", "caching"},
			createFlags{rest: []string{"add", "caching"}}},
		{"from + template", []string{"--from", "develop", "-t", "spike", "fix", "it"},
			createFlags{base: "develop", template: "spike", rest: []string{"fix", "it"}}},
		{"equals forms", []string{"--from=develop", "--template=spike", "--branch=feature/foo"},
			createFlags{base: "develop", template: "spike", branch: "feature/foo", branchSet: true, rest: nil}},
		{"branch", []string{"--branch", "feature/foo"},
			createFlags{branch: "feature/foo", branchSet: true, rest: nil}},
		{"checkout alias", []string{"--checkout", "origin/feature/foo"},
			createFlags{branch: "origin/feature/foo", branchSet: true, rest: nil}},
		{"branch keeps hint separate", []string{"--branch", "feature/foo", "stray", "hint"},
			createFlags{branch: "feature/foo", branchSet: true, rest: []string{"stray", "hint"}}},
		// Present but empty has to stay distinguishable from absent, or runCreate
		// reads it as no --branch and opens the New tab instead of erroring.
		{"branch= empty is still set", []string{"--branch="},
			createFlags{branch: "", branchSet: true, rest: nil}},
		{"checkout= empty is still set", []string{"--checkout="},
			createFlags{branch: "", branchSet: true, rest: nil}},
		{"roles", []string{"--roles", "logic,assets", "build", "it"},
			createFlags{roles: []string{"logic", "assets"}, rest: []string{"build", "it"}}},
		{"roles= with spaces", []string{"--roles=logic, assets"},
			createFlags{roles: []string{"logic", "assets"}, rest: nil}},
		{"role singular alias", []string{"-r", "logic"},
			createFlags{roles: []string{"logic"}, rest: nil}},
	}
	for _, c := range cases {
		got := extractCreateFlags(c.args)
		if got.base != c.want.base || got.template != c.want.template || got.branch != c.want.branch {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
		if got.branchSet != c.want.branchSet {
			t.Errorf("%s: branchSet = %v, want %v", c.name, got.branchSet, c.want.branchSet)
		}
		if strings.Join(got.rest, " ") != strings.Join(c.want.rest, " ") {
			t.Errorf("%s: rest = %q, want %q", c.name, got.rest, c.want.rest)
		}
		if strings.Join(got.roles, ",") != strings.Join(c.want.roles, ",") {
			t.Errorf("%s: roles = %q, want %q", c.name, got.roles, c.want.roles)
		}
	}
}
