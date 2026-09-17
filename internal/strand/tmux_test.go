package strand

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// spawned starts a throwaway strand and guarantees it is gone afterwards, so a
// failing test never leaves a tmux session behind on the machine.
func spawned(t *testing.T, role string, argv ...string) (taskID string) {
	t.Helper()
	if !Available() {
		t.Skip("tmux not installed")
	}
	taskID = "test" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if err := Spawn(taskID, role, t.TempDir(), argv, 80, 24); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Kill(taskID, role) })
	return taskID
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A running strand is alive and reports running. This is the state norn shows
// while an agent works.
func TestSpawnedStrandIsRunning(t *testing.T) {
	id := spawned(t, "logic", "sh", "-c", "sleep 30")

	if !Alive(id, "logic") {
		t.Fatal("a spawned strand is not alive")
	}
	st, err := Read(id, "logic")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Running || st.Exited {
		t.Fatalf("status = %+v, want running", st)
	}
}

// The exit code survives the agent. Without remain-on-exit tmux reaps the
// session and the code is gone, which is the difference between "landed
// cleanly" and "nobody knows".
func TestExitCodeSurvivesTheAgent(t *testing.T) {
	id := spawned(t, "logic", "sh", "-c", "exit 4")

	waitFor(t, "the agent to exit", func() bool {
		st, err := Read(id, "logic")
		return err == nil && st.Exited
	})
	st, err := Read(id, "logic")
	if err != nil {
		t.Fatal(err)
	}
	if st.ExitCode != 4 {
		t.Fatalf("exit code = %d, want 4", st.ExitCode)
	}
	if !Alive(id, "logic") {
		t.Fatal("the session vanished with its agent, so nothing can read the result")
	}
}

// The strand runs where it was told to. A strand is a worktree's agent, so a
// session in the wrong directory is an agent working on the wrong branch.
func TestStrandRunsInItsWorktree(t *testing.T) {
	if !Available() {
		t.Skip("tmux not installed")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran-here")
	id := "test" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if err := Spawn(id, "logic", dir, []string{"sh", "-c", "pwd > ran-here"}, 80, 24); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Kill(id, "logic") })

	waitFor(t, "the strand to write its marker", func() bool {
		_, err := os.Stat(marker)
		return err == nil
	})
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if want := dir; !strings.Contains(string(got), filepath.Base(want)) {
		t.Fatalf("strand ran in %q, want %q", strings.TrimSpace(string(got)), want)
	}
}

// Killing is idempotent, and List only ever reports norn's own sessions: a
// clean-up that killed someone's editor session would be unforgivable.
func TestKillAndList(t *testing.T) {
	id := spawned(t, "assets", "sh", "-c", "sleep 30")

	names, err := List()
	if err != nil {
		t.Fatal(err)
	}
	want := Name(id, "assets")
	found := false
	for _, n := range names {
		if n == want {
			found = true
		}
		if !strings.HasPrefix(n, prefix+"-") {
			t.Fatalf("List returned a session norn does not own: %q", n)
		}
	}
	if !found {
		t.Fatalf("List did not include %q: %v", want, names)
	}

	if err := Kill(id, "assets"); err != nil {
		t.Fatal(err)
	}
	if Alive(id, "assets") {
		t.Fatal("the session outlived its kill")
	}
	if err := Kill(id, "assets"); err != nil {
		t.Fatalf("killing an already dead strand reported an error: %v", err)
	}
}

// Reading a strand nobody spawned says so, rather than looking like a running
// one, since a restart asks this about every row it finds in the store.
func TestReadMissingSession(t *testing.T) {
	if !Available() {
		t.Skip("tmux not installed")
	}
	if _, err := Read("nosuchtask", "logic"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("Read on a missing session = %v, want ErrNoSession", err)
	}
}

// The attach command has to be one this tmux accepts. A flag in the wrong
// position is rejected by the whole command, and the pane then shows tmux's
// complaint where the agent should be, which is how `-u` after the subcommand
// escaped: it is valid as a global flag and invalid here.
func TestAttachCmdIsAccepted(t *testing.T) {
	id := spawned(t, "logic", "sh", "-c", "sleep 30")

	cmd := AttachCmd(id, "logic")
	// Attaching needs a real terminal a test does not have, so run the same
	// global flags with a harmless subcommand: tmux parses flags before it does
	// anything, which is exactly what went wrong when `-u` sat after the
	// subcommand.
	var globals []string
	for _, a := range cmd.Args[1:] {
		if a == "attach-session" {
			break
		}
		globals = append(globals, a)
	}
	out, err := exec.Command("tmux", append(globals, "list-sessions")...).CombinedOutput()
	if err != nil {
		t.Fatalf("tmux rejected the global flags %v: %v: %s", globals, err, out)
	}
	if !strings.Contains(string(out), Name(id, "logic")) {
		t.Fatalf("the attach command points at another server: %s", out)
	}
}

// A note reaches another strand as typed input, which is the whole channel: no
// protocol, no inbox, and it works for any agent because a terminal is the one
// thing they all have.
func TestSendReachesTheStrand(t *testing.T) {
	id := spawned(t, "logic", "sh", "-c", "read line; printf 'heard:%s' \"$line\"; sleep 30")

	waitFor(t, "the shell to be reading", func() bool {
		st, err := Read(id, "logic")
		return err == nil && st.Running
	})
	if err := Send(id, "logic", "[design] renamed the export to Weave"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the strand to receive the note", func() bool {
		out, _ := exec.Command("tmux", "-L", socket, "capture-pane", "-p", "-t", Name(id, "logic")).Output()
		return strings.Contains(string(out), "heard:[design] renamed the export to Weave")
	})
}

// Sending to a strand that is not there fails by name rather than silently
// dropping the note, since the sender would otherwise assume it landed.
func TestSendToMissingStrand(t *testing.T) {
	if !Available() {
		t.Skip("tmux not installed")
	}
	if err := Send("nosuchtask", "logic", "hello"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("Send to a missing strand = %v, want ErrNoSession", err)
	}
}
