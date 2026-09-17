package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/sandbye/norn/internal/config"
	"github.com/sandbye/norn/internal/paths"
	"github.com/sandbye/norn/internal/state"
	"github.com/sandbye/norn/internal/taskrun"
)

// runTask handles `norn run [<task-id>]`: drive a split task's non-integrating
// roles headless and merge each into the trunk when it exits. A verb rather
// than something the TUI owns, because the orchestrator this design ends in
// drives the same verbs a person types.
func runTask(cfg config.Config, args []string) {
	taskID := ""
	for _, a := range args {
		if a == "-h" || a == "--help" {
			fmt.Println("usage: norn run [<task-id>]   (no id → the task this worktree belongs to)")
			return
		}
		taskID = a
	}
	if taskID == "" {
		id, err := taskFromCwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		taskID = id
	}

	// SIGINT cancels the context, which kills the role processes: they are
	// children of this run, and leaving them writing into worktrees nobody is
	// watching is worse than stopping them.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := taskrun.Run(ctx, taskrun.Options{Cfg: cfg, TaskID: taskID, Out: os.Stdout}); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// taskFromCwd resolves the task from the worktree the shell is in, so the
// common case is a bare `norn run` inside any of the task's worktrees.
func taskFromCwd() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	store, err := state.Load()
	if err != nil {
		return "", err
	}
	sess := store.FindByPath(paths.Canon(cwd))
	if sess == nil {
		return "", fmt.Errorf("no norn worktree at %s, so pass a task id", cwd)
	}
	if sess.TaskID == "" {
		return "", fmt.Errorf("%s is a standalone worktree, not part of a split task", sess.Branch)
	}
	return sess.TaskID, nil
}
