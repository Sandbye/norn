package main

// `norn statusline` renders a compact re-entry card for Claude Code's status
// bar: the branch, what you were about to do, and what is blocking it.
//
// The point is the cost asymmetry. Coming back to a thread means rebuilding
// "where was I", and today that happens either in your head or by spending
// model tokens to rediscover it. The status bar is the one surface that can
// carry it for free: the script runs locally and the model never sees it.
//
// Claude Code delivers session JSON on stdin and prints whatever this writes.
// It runs on every render, so this never blocks, never panics, and prints
// something useful even when every input is missing.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/sandbye/norn/internal/git"
	"github.com/sandbye/norn/internal/prompt"
)

// statuslineInput is the subset of Claude Code's status JSON norn reads.
// Everything else in the envelope is ignored on purpose, so a format change
// degrades to a thinner card rather than an error.
type statuslineInput struct {
	Cwd       string `json:"cwd"`
	Workspace struct {
		CurrentDir string `json:"current_dir"`
	} `json:"workspace"`
	ContextWindow struct {
		UsedPercentage *float64 `json:"used_percentage"`
	} `json:"context_window"`
}

// Nord, matching the TUI so the two read as one tool.
const (
	ansiReset  = "\x1b[0m"
	ansiBranch = "\x1b[38;2;136;192;208m" // frost blue
	ansiNext   = "\x1b[38;2;235;203;139m" // aurora yellow
	ansiBlock  = "\x1b[38;2;191;97;106m"  // aurora red
	ansiDim    = "\x1b[38;2;76;86;106m"   // polar night
)

// statuslineMax caps each line. The status bar has no width in the payload, so
// this is a readable default rather than a fit.
const statuslineMax = 100

func cmdStatusline() {
	var in statuslineInput
	// A decode failure is not worth reporting: an empty struct still renders
	// the git half of the card.
	_ = json.NewDecoder(os.Stdin).Decode(&in)

	dir := in.Workspace.CurrentDir
	if dir == "" {
		dir = in.Cwd
	}
	if dir == "" {
		dir, _ = os.Getwd()
	}

	root, err := git.RepoRootAt(dir)
	if err != nil || root == "" {
		return // not in a repo: print nothing rather than a broken card
	}

	fmt.Print(statuslineCard(root, git.CurrentBranch(root), readStateFile(root), in.ContextWindow.UsedPercentage))
}

// readStateFile returns the worktree's .state.md, or "" when it has none.
func readStateFile(root string) string {
	data, err := os.ReadFile(root + "/.state.md")
	if err != nil {
		return ""
	}
	return string(data)
}

// statuslineCard builds the rendered card. Split from cmdStatusline so the
// layout is testable without a git repo or a real session on stdin.
func statuslineCard(root, branch, stateMD string, ctxPct *float64) string {
	if branch == "" {
		branch = shortPath(root)
	}

	head := ansiBranch + branch + ansiReset
	if ctxPct != nil {
		head += ansiDim + fmt.Sprintf("  ctx %.0f%%", *ctxPct) + ansiReset
	}

	// No .state.md is the common case outside a norn thread, and a bare branch
	// line is still better than nothing.
	if stateMD == "" {
		return head + "\n"
	}

	var lines []string
	lines = append(lines, head)

	if next := prompt.ExtractNext(stateMD); next != "" {
		lines = append(lines, ansiNext+"→ "+ansiReset+truncate(next, statuslineMax))
	} else if goal := prompt.ExtractGoal(stateMD); goal != "" {
		lines = append(lines, ansiDim+truncate(goal, statuslineMax)+ansiReset)
	}
	if blocked := prompt.ExtractBlocked(stateMD); blocked != "" {
		lines = append(lines, ansiBlock+"⨯ "+truncate(blocked, statuslineMax)+ansiReset)
	}
	return strings.Join(lines, "\n") + "\n"
}

// truncate cuts on runes, not bytes, so a multi-byte character can't be split
// into a replacement glyph in the middle of the bar.
func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return strings.TrimRight(string(r[:max-1]), " ") + "…"
}

// shortPath is the fallback label when HEAD is detached: the directory name.
func shortPath(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 && i+1 < len(p) {
		return p[i+1:]
	}
	return p
}
