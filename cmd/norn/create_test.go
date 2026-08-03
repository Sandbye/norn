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
			createFlags{base: "develop", template: "spike", branch: "feature/foo", rest: nil}},
		{"branch", []string{"--branch", "feature/foo"},
			createFlags{branch: "feature/foo", rest: nil}},
		{"checkout alias", []string{"--checkout", "origin/feature/foo"},
			createFlags{branch: "origin/feature/foo", rest: nil}},
		{"branch keeps hint separate", []string{"--branch", "feature/foo", "stray", "hint"},
			createFlags{branch: "feature/foo", rest: []string{"stray", "hint"}}},
	}
	for _, c := range cases {
		got := extractCreateFlags(c.args)
		if got.base != c.want.base || got.template != c.want.template || got.branch != c.want.branch {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
		if strings.Join(got.rest, " ") != strings.Join(c.want.rest, " ") {
			t.Errorf("%s: rest = %q, want %q", c.name, got.rest, c.want.rest)
		}
	}
}
