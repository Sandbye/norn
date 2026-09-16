package main

import (
	"os"
	"strings"
	"testing"
)

// The wrapper has to be named after the binary that printed it. Named "norn"
// regardless, `norn-dev shell-init` defines a function that calls the release
// binary, and the dev build silently never cd's.
func TestShellInitScriptNamesTheBinary(t *testing.T) {
	for _, shell := range []string{"zsh", "bash", "sh", "fish"} {
		got := shellInitScript(shell, "norn-dev", "/cache/norn")

		if !strings.Contains(got, "norn-dev") {
			t.Errorf("%s: wrapper does not name the binary:\n%s", shell, got)
		}
		if strings.Contains(got, "command norn ") || strings.Contains(got, "command norn\n") {
			t.Errorf("%s: wrapper calls the release binary:\n%s", shell, got)
		}
		if !strings.Contains(got, "/cache/norn/cd-target-") {
			t.Errorf("%s: cache dir not baked in:\n%s", shell, got)
		}
	}

	// fish gets its own syntax, not the posix one.
	if f := shellInitScript("fish", "norn", "/c"); !strings.HasPrefix(f, "function norn") {
		t.Errorf("fish wrapper is not fish syntax:\n%s", f)
	}
	if p := shellInitScript("zsh", "norn", "/c"); !strings.HasPrefix(p, "norn() {") {
		t.Errorf("posix wrapper is wrong:\n%s", p)
	}
}

func TestBinaryNameFallsBackOnOddArgv(t *testing.T) {
	orig := os.Args[0]
	t.Cleanup(func() { os.Args[0] = orig })

	for _, c := range []struct{ argv, want string }{
		{"/usr/local/bin/norn", "norn"},
		{"/Users/x/go/bin/norn-dev", "norn-dev"},
		{"-zsh", "norn"}, // login shells prefix argv[0] with a dash
		{"/", "norn"},
		{"", "norn"},
	} {
		os.Args[0] = c.argv
		if got := binaryName(); got != c.want {
			t.Errorf("argv[0]=%q: binaryName = %q, want %q", c.argv, got, c.want)
		}
	}
}
