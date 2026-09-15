package git

import (
	"errors"
	"testing"
	"time"
)

func TestTimeoutForPicksBudgetFromSubcommand(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want time.Duration
	}{
		{"network op", []string{"fetch", "--prune"}, NetworkTimeout},
		{"network op behind flags", []string{"-C", "/tmp", "push", "-u"}, NetworkTimeout},
		{"network op behind a joined flag value", []string{"--git-dir=/tmp/x", "fetch"}, NetworkTimeout},
		{"local op", []string{"status", "--porcelain"}, LocalTimeout},
		{"flag value is not the subcommand", []string{"-c", "push.default=simple", "status"}, LocalTimeout},
		{"unknown op falls back to local", []string{"cat-file", "-p", "HEAD"}, LocalTimeout},
		{"no args", nil, LocalTimeout},
	}
	for _, c := range cases {
		if got := timeoutFor(c.args); got != c.want {
			t.Errorf("%s: timeoutFor(%v) = %s, want %s", c.name, c.args, got, c.want)
		}
	}
}

// A remote that accepts the connection and then never answers is the case that
// used to hang the TUI forever: no output, no error, no exit.
func TestCmdRunKillsAHangingCommand(t *testing.T) {
	old := LocalTimeout
	LocalTimeout = 150 * time.Millisecond
	t.Cleanup(func() { LocalTimeout = old })

	start := time.Now()
	err := cmdRun(t.TempDir(), "sleep", "30")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("hanging command returned no error")
	}
	if !errors.Is(err, ErrTimedOut) {
		t.Errorf("err = %v, want ErrTimedOut", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("took %s, want the command killed at its budget", elapsed)
	}
}
