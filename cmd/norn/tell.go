package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/sandbye/norn/internal/state"
	"github.com/sandbye/norn/internal/strand"
)

// tellStrand handles `norn tell <role> <text>`: deliver one line to another
// strand of this task.
//
// One way and interrupting, by design. It exists so a strand can say the thing
// the others cannot discover on their own until the merge, such as an interface
// it changed or a file it now owns. Anything that wants a conversation belongs
// in the person's hands.
func tellStrand(args []string) {
	if len(args) < 2 || args[0] == "-h" || args[0] == "--help" {
		fmt.Println(`usage: norn tell <role> <message>   (from inside any strand of the task)`)
		if len(args) < 2 {
			os.Exit(1)
		}
		return
	}
	role, text := args[0], strings.TrimSpace(strings.Join(args[1:], " "))
	if text == "" {
		fmt.Fprintln(os.Stderr, "error: nothing to say")
		os.Exit(1)
	}

	taskID, from, err := strandFromCwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if role == from {
		fmt.Fprintf(os.Stderr, "error: %s is this strand\n", role)
		os.Exit(1)
	}
	// The sender is named in the message: a line that arrives in a pane with no
	// author reads as the person typing, and the strand answers the wrong one.
	if err := strand.Send(taskID, role, "["+from+"] "+text); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("told %s\n", role)
}

// strandFromCwd resolves which strand is asking, from the worktree it runs in.
func strandFromCwd() (taskID, role string, err error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", "", err
	}
	store, err := state.Load()
	if err != nil {
		return "", "", err
	}
	sess := store.FindByPath(cwd)
	if sess == nil || sess.TaskID == "" || sess.Role == "" {
		return "", "", fmt.Errorf("%s is not a strand of a task", cwd)
	}
	return sess.TaskID, sess.Role, nil
}
