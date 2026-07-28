package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/sandbye/norn/internal/claude"
	"github.com/sandbye/norn/internal/config"
	"github.com/sandbye/norn/internal/git"
	"github.com/sandbye/norn/internal/prompt"
	"github.com/sandbye/norn/internal/state"
	"github.com/sandbye/norn/internal/task"
	"github.com/sandbye/norn/internal/tui"
)

// version is the release version, injected at build time via
// -ldflags "-X main.version=...". "dev" for local builds.
var version = "dev"

func main() {
	repoRoot, _ := git.RepoRoot()
	cfg, err := config.Load(repoRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}
	tui.ApplyTheme(cfg.Theme)
	if d := cfg.Templates.Dir; d != "" {
		if strings.HasPrefix(d, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				d = filepath.Join(home, d[2:])
			}
		}
		prompt.SetTemplateDir(d)
	}

	args := os.Args[1:]

	// Direct subcommands (non-TUI)
	if len(args) > 0 {
		switch args[0] {
		case "--list":
			cmdList(repoRoot)
			return
		case "--status":
			cmdStatus(cfg, repoRoot)
			return
		case "--project-config":
			cmdProjectConfig(cfg, repoRoot)

		case "context", "--context":
			cmdContext(repoRoot)
			return
		case "--templates":
			cmdTemplates(cfg)
			return
		case "template":
			switch {
			case len(args) >= 3 && args[1] == "new":
				cmdTemplateNew(args[2])
			case len(args) >= 2 && args[1] == "edit":
				name := "task"
				if len(args) >= 3 {
					name = args[2]
				}
				cmdTemplateEdit(name)
			default:
				cmdTemplates(cfg)
			}
			return
		case "shell-init":
			shell := ""
			if len(args) > 1 {
				shell = args[1]
			}
			cmdShellInit(shell)
			return
		case "auth", "login":
			cmdAuth(args[1:])
			return
		case "settings", "--settings":
			runApp(cfg, repoRoot, tui.ViewSettings)
			return
		case "--activity-tick":
			cmdActivityTick(repoRoot)
			return
		case "--dashboard":
			runApp(cfg, repoRoot, tui.ViewThreads)
			return
		case "--refresh-docs":
			cmdRefreshDocs()
			return
		case "--diff", "diff":
			plain := false
			list := false
			sinceReview := false
			working := false
			prNum := ""
			baseOverride := ""
			diffArgs := args[1:]
			for i := 0; i < len(diffArgs); i++ {
				a := diffArgs[i]
				switch {
				case a == "--plain" || a == "-p":
					plain = true
				case a == "--list" || a == "-l":
					list = true
				case a == "--working" || a == "--wip" || a == "-w":
					working = true
				case a == "--since-review":
					sinceReview = true
				case a == "--base" || a == "-b":
					// `--base <ref>` — peek next arg (may be omitted → fork base).
					if i+1 < len(diffArgs) && !strings.HasPrefix(diffArgs[i+1], "-") {
						baseOverride = diffArgs[i+1]
						i++
					}
				case strings.HasPrefix(a, "--base="):
					baseOverride = strings.TrimPrefix(a, "--base=")
				case strings.HasPrefix(a, "-b="):
					baseOverride = strings.TrimPrefix(a, "-b=")
				case strings.HasPrefix(a, "#"):
					prNum = strings.TrimPrefix(a, "#")
				case isAllDigits(a):
					prNum = a
				}
			}
			if list {
				cmdDiffList(cfg, repoRoot, plain)
				return
			}
			if prNum != "" {
				cmdDiffPR(cfg, repoRoot, prNum, plain, sinceReview)
				return
			}
			// Bare `norn diff` → the whole branch (fork base ...HEAD, committed,
			// pushed or not). `-w`/`--working` → just current uncommitted changes.
			// `--base [ref]` → compare against a specific ref instead of the fork base.
			cmdDiff(cfg, repoRoot, plain, baseOverride, working)
			return
		case "create", "new", "c":
			runCreate(cfg, repoRoot, args[1:])
			return
		case "review":
			runReview(cfg, repoRoot, args[1:])
			return
		case "init":
			cmdInit(repoRoot)
			return
		case "--doctor", "doctor":
			cmdDoctor(cfg, repoRoot)
			return
		case "--help", "-h":
			cmdHelp()
			return
		case "--version", "-v", "version":
			fmt.Printf("norn %s\n", version)
			return
		}
	}

	// Determine the initial tab / view.
	initialView := tui.ViewThreads
	switch {
	case len(args) > 0 && args[0] == "--clean":
		initialView = tui.ViewClean
	case len(args) > 0 && args[0] == "--cd":
		initialView = tui.ViewCd
		// `-d`/`--dir` are legacy aliases; the dashboard is the hub now, so they
		// just open on Threads like bare `norn`.
	}

	// Only --clean/--cd/-d/--dir open the TUI here; any other leading token is an
	// unknown command. Creating a worktree now requires the explicit `create`
	// verb (bare `norn foo` used to create silently, which was easy to trigger by
	// mistyping a subcommand).
	if len(args) > 0 && args[0] != "--clean" && args[0] != "--cd" && args[0] != "-d" && args[0] != "--dir" {
		fmt.Fprintf(os.Stderr, "norn: unknown command %q\n\nDid you mean:  norn create %q\nRun `norn --help` for all commands.\n", args[0], args[0])
		os.Exit(1)
	}

	runApp(cfg, repoRoot, initialView)
}

// runApp runs the unified tabbed TUI at the given view and performs whatever the
// user chose on exit (launch, resume, or cd).
func runApp(cfg config.Config, repoRoot string, initialView tui.View) {
	reapStale(repoRoot)

	scope := ""
	if repoRoot != "" {
		scope = originRepoName(repoRoot)
	}
	app := tui.NewApp(cfg, repoRoot, scope, initialView)
	p := tea.NewProgram(app, tea.WithAltScreen())
	m, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	result := m.(tui.App).Result()
	switch result.Action {
	case tui.ResultLaunch:
		upsertSessionFromPath(repoRoot, cfg.WorktreeDir, result.Path)
		writeCdTarget(result.Path)
		clearScreen()
		tui.LaunchAgent(cfg.Agent, result.Path, false, result.Model)
	case tui.ResultResume:
		upsertSessionFromPath(repoRoot, cfg.WorktreeDir, result.Path)
		writeCdTarget(result.Path)
		clearScreen()
		tui.LaunchAgent(cfg.Agent, result.Path, true, result.Model)
	case tui.ResultCd:
		// Parent-shell cd: write the target and exit. The shell wrapper cd's the
		// current shell into it — no nested subshell. Bump activity so the
		// worktree you just looked at sorts to the top next time.
		upsertSessionFromPath(repoRoot, cfg.WorktreeDir, result.Path)
		writeCdTarget(result.Path)
		clearScreen()
	}
}

// upsertSessionFromPath records a session row for a worktree we created or
// resumed via the TUI, where we don't have hint + branch passed back to us.
// We derive them from the worktree itself.
func upsertSessionFromPath(repoRoot, worktreeDir, wtPath string) {
	if repoRoot == "" || wtPath == "" {
		return
	}
	branch := strings.TrimSpace(currentBranch(wtPath))
	if branch == "" {
		return
	}
	kind := "task"
	if rel, err := filepath.Rel(worktreeDir, wtPath); err == nil {
		parts := strings.SplitN(rel, string(filepath.Separator), 2)
		if len(parts) > 0 && (parts[0] == "task" || parts[0] == "review") {
			kind = parts[0]
		}
	}
	hint := readHintFromWorktreeMD(wtPath)
	upsertSession(repoRoot, kind, branch, wtPath, hint)
}

func readHintFromWorktreeMD(wtPath string) string {
	data, err := os.ReadFile(filepath.Join(wtPath, ".worktree.md"))
	if err != nil {
		return ""
	}
	return prompt.ExtractHint(string(data))
}

// reapStale silently prunes ghost `.git/worktrees/` entries so branches whose
// worktree dir is gone don't keep their refs locked. Actual stale-worktree
// cleanup is surfaced inside the TUI Clean view (gone-from-remote rows are
// pre-selected on entry).
func reapStale(repoRoot string) {
	reapCdTargets()
	if repoRoot == "" {
		return
	}
	_ = git.PruneWorktrees(repoRoot)
}

// reapCdTargets deletes cd-target files older than an hour. The shell wrapper
// consumes and removes its own target within seconds of norn exiting, so
// anything left this long belongs to a dead shell (or a run with no wrapper).
func reapCdTargets() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	dir := filepath.Join(home, ".cache", "work")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-time.Hour)
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "cd-target-") {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// aiResolveBranch is MakeBranch, but when the deterministic name lacks a
// descriptive slug (e.g. created from a bare ClickUp id) it asks Claude for a
// better one. Gated on config + `claude` availability; falls back silently.
func aiResolveBranch(cfg config.Config, repoRoot, kind, hint string) string {
	branch := git.MakeBranch(kind, hint)
	if cfg.AINaming && cfg.HeadlessClaude() && claude.Available() && git.BranchLacksSlug(branch) {
		fmt.Println("Naming the worktree via Claude…")
		branch = claude.EnrichBranchName(context.Background(), repoRoot, hint, branch)
	}
	return branch
}

// runCreate handles `norn create [--from x] [--template y] "hint"`. createArgs
// is everything after the `create` verb. A bare `norn create` (no hint) opens
// the New tab instead of creating blindly. (PR review is its own verb: `norn
// review <pr#>`.)
func runCreate(cfg config.Config, repoRoot string, createArgs []string) {
	if repoRoot == "" {
		fmt.Fprintln(os.Stderr, "error: not inside a git repository")
		os.Exit(1)
	}
	baseOverride, templateOverride, rest := extractCreateFlags(createArgs)
	hint := strings.Join(rest, " ")
	if strings.TrimSpace(hint) == "" {
		// No hint given → let the user compose it in the New tab.
		runApp(cfg, repoRoot, tui.ViewCreate)
		return
	}
	directCreate(cfg, repoRoot, "task", hint, baseOverride, templateOverride)
}

func directCreate(cfg config.Config, repoRoot, kind, hint, baseOverride, templateOverride string) {
	// Resolve the *branch base* (source to fork from) by priority:
	//   1. explicit --from override
	//   2. branch_base from project config (production-line, may differ from PR target)
	//   3. pr_base from project config (when single-base workflow)
	//   4. git's detected default (origin/HEAD) — works in any repo
	//   5. "main" as last-resort guess
	base := resolveBranchBase(cfg, repoRoot, baseOverride)

	branch := aiResolveBranch(cfg, repoRoot, kind, hint)
	fmt.Printf("Creating worktree: %s (base: %s)\n", branch, base)

	wtPath, err := git.CreateWorktree(repoRoot, cfg.WorktreeDir, branch, base)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	_ = git.SymlinkEnvFiles(repoRoot, wtPath)

	// Generate prompt
	tmpl := prompt.Resolve(cfg, kind, templateOverride)
	if templateOverride != "" && !prompt.Has(templateOverride) {
		fmt.Fprintf(os.Stderr, "warning: template %q not found, using %q\n", templateOverride, tmpl)
	}
	promptText, err := prompt.Render(cfg, kind, hint, base, tmpl, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not render prompt: %v\n", err)
	}
	if err := os.WriteFile(wtPath+"/.worktree.md", []byte(promptText), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not write prompt: %v\n", err)
	}

	upsertSession(repoRoot, kind, branch, wtPath, hint)
	writeCdTarget(wtPath)

	clearScreen()
	tui.LaunchAgent(cfg.Agent, wtPath, false, "") // config default model
}

// runReview handles `norn review <pr#>`: check the PR's head out into a
// worktree and launch the agent with a PR-aware review brief. Read-only: the
// branch is never pushed and the brief tells the agent to comment, not commit.
func runReview(cfg config.Config, repoRoot string, reviewArgs []string) {
	if repoRoot == "" {
		fmt.Fprintln(os.Stderr, "error: not inside a git repository")
		os.Exit(1)
	}
	if len(reviewArgs) == 0 {
		fmt.Fprintln(os.Stderr, "usage: norn review <pr#>")
		os.Exit(1)
	}
	prNum := strings.TrimPrefix(strings.TrimSpace(reviewArgs[0]), "#")
	if !isAllDigits(prNum) {
		fmt.Fprintf(os.Stderr, "error: %q is not a PR number\n", reviewArgs[0])
		os.Exit(1)
	}

	pr, err := fetchReviewPR(repoRoot, prNum)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	branch := fmt.Sprintf("review/pr-%d", pr.Number)
	wtPath := filepath.Join(cfg.WorktreeDir, branch)

	if _, statErr := os.Stat(wtPath); statErr == nil {
		// Already reviewing this PR — just jump back into the existing worktree.
		fmt.Printf("Review worktree for PR #%d already exists at %s\n", pr.Number, wtPath)
	} else {
		fmt.Printf("Reviewing PR #%d: %s (base: %s)\n", pr.Number, pr.Title, pr.Base)
		if err := git.FetchPRHead(repoRoot, prNum, branch); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		wtPath, err = git.AddWorktreeFromRef(repoRoot, cfg.WorktreeDir, branch)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		_ = git.SymlinkEnvFiles(repoRoot, wtPath)

		promptText, rerr := prompt.RenderReview(cfg, "", &pr)
		if rerr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not render review brief: %v\n", rerr)
		}
		if werr := os.WriteFile(wtPath+"/.worktree.md", []byte(promptText), 0o644); werr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not write brief: %v\n", werr)
		}
	}

	upsertSession(repoRoot, "review", branch, wtPath, pr.Title)
	writeCdTarget(wtPath)

	clearScreen()
	tui.LaunchAgent(cfg.Agent, wtPath, false, "")
}

// fetchReviewPR resolves the PR fields needed for a review worktree + brief.
func fetchReviewPR(repoRoot, prNum string) (prompt.PRRef, error) {
	cmd := exec.Command("gh", "pr", "view", prNum, "--json", "number,title,url,baseRefName")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return prompt.PRRef{}, fmt.Errorf("gh pr view #%s (is gh installed + authed, and does this PR exist on origin?): %w", prNum, err)
	}
	var raw struct {
		Number      int    `json:"number"`
		Title       string `json:"title"`
		URL         string `json:"url"`
		BaseRefName string `json:"baseRefName"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return prompt.PRRef{}, fmt.Errorf("parse pr view: %w", err)
	}
	return prompt.PRRef{Number: raw.Number, Title: raw.Title, URL: raw.URL, Base: raw.BaseRefName}, nil
}

// writeCdTarget records the path the shell wrapper should `cd` into after the
// work binary exits. Scoped per-shell via the parent PID so concurrent `work`
// invocations in other terminals can't bleed into each other. Read by the
// `work()` shell function (in .zshrc/.bashrc) then deleted by it.
func writeCdTarget(path string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	dir := filepath.Join(home, ".cache", "work")
	_ = os.MkdirAll(dir, 0o755)
	target := filepath.Join(dir, fmt.Sprintf("cd-target-%d", os.Getppid()))
	_ = os.WriteFile(target, []byte(path), 0o644)
}

func cmdList(repoRoot string) {
	if repoRoot == "" {
		fmt.Fprintln(os.Stderr, "error: not inside a git repository")
		os.Exit(1)
	}
	cmd := git.CmdOutputPublic(repoRoot, "git", "worktree", "list")
	fmt.Println(cmd)
}

func cmdStatus(cfg config.Config, repoRoot string) {
	common := ""
	if repoRoot != "" {
		c, _ := git.CommonDir(repoRoot)
		common = c
	}

	wts, err := git.ListWorktrees(cfg.WorktreeDir, common)
	if err != nil || len(wts) == 0 {
		fmt.Println("No worktrees found.")
		return
	}

	for _, wt := range wts {
		fmt.Printf("\033[1;34m%s\033[0m\n", wt.Branch)
		if wt.CommitMsg != "" {
			fmt.Printf("  Last:  %s (%s)\n", wt.CommitMsg, git.Age(wt.LastCommit))
		}
		fmt.Printf("  Path:  %s\n\n", wt.Path)
	}
}

// cmdContext prints a compact digest of the OTHER active worktrees in the
// current repo: what parallel threads are in flight, so a session isn't blind
// to the rest of the tree. Read-only, sourced from the session store. Silent
// (no output) outside a repo or when there are no sibling worktrees, so a hook
// can inject its stdout unconditionally.
func cmdContext(repoRoot string) {
	if repoRoot == "" {
		return
	}
	scope := originRepoName(repoRoot)
	store, err := state.Load()
	if err != nil || store == nil {
		return
	}
	store.SortByActivity()

	cur := repoRoot
	if abs, err := filepath.EvalSymlinks(repoRoot); err == nil {
		cur = abs
	}
	var sibs []state.Session
	for _, s := range store.Sessions {
		if s.Repo != scope || s.Status != state.StatusActive {
			continue
		}
		p := s.Path
		if abs, err := filepath.EvalSymlinks(p); err == nil {
			p = abs
		}
		if p == cur {
			continue // this worktree
		}
		if git.CheckoutClass(s.Path) != "worktree" {
			continue // gone / not a live worktree
		}
		sibs = append(sibs, s)
	}
	if len(sibs) == 0 {
		return
	}

	fmt.Println("## Other active worktrees (this repo)")
	fmt.Println()
	for _, s := range sibs {
		label := s.Title
		if label == "" {
			label = s.Branch
		}
		line := fmt.Sprintf("- %s  `%s`", label, s.Branch)
		if s.PRNumber > 0 {
			line += fmt.Sprintf(" · PR #%d", s.PRNumber)
		}
		line += " · " + git.Age(s.LastActivityAt)
		fmt.Println(line)
	}
}

// cmdActivityTick bumps last_activity_at for the session matching the current
// repo + branch. Called by the activity-log.py hook on every Claude tool use.
// Silent on no-op; never fails the caller.
// extractFromFlag pulls `--from <branch>` or `-b <branch>` out of args. Returns
// the chosen base (or "" if absent) and the remaining args in original order.
// extractCreateFlags pulls `--from`/`-b <branch>` and `--template`/`-t <name>`
// out of the create args so the remainder flows cleanly into the hint.
func extractCreateFlags(args []string) (base, template string, rest []string) {
	rest = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case (a == "--from" || a == "-b") && i+1 < len(args):
			base = args[i+1]
			i++ // skip the value
		case strings.HasPrefix(a, "--from="):
			base = strings.TrimPrefix(a, "--from=")
		case (a == "--template" || a == "-t") && i+1 < len(args):
			template = args[i+1]
			i++ // skip the value
		case strings.HasPrefix(a, "--template="):
			template = strings.TrimPrefix(a, "--template=")
		default:
			rest = append(rest, a)
		}
	}
	return base, template, rest
}

// isAllDigits returns true if s consists solely of ASCII digits and is non-empty.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// cmdDiffList shows a picker of open PRs in the current repo. On selection,
// transitions to cmdDiffPR for the chosen PR.
func cmdDiffList(cfg config.Config, repoRoot string, plain bool) {
	if repoRoot == "" {
		fmt.Fprintln(os.Stderr, "error: not inside a git repository (gh needs repo context)")
		os.Exit(1)
	}

	out, err := exec.Command("gh", "pr", "list",
		"--state", "open",
		"--limit", "50",
		"--json", "number,title,author,baseRefName,headRefName,isDraft,updatedAt").Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: gh pr list failed: %v\n", err)
		os.Exit(1)
	}

	var raw []struct {
		Number      int                    `json:"number"`
		Title       string                 `json:"title"`
		Author      struct{ Login string } `json:"author"`
		BaseRefName string                 `json:"baseRefName"`
		HeadRefName string                 `json:"headRefName"`
		IsDraft     bool                   `json:"isDraft"`
		UpdatedAt   time.Time              `json:"updatedAt"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		fmt.Fprintf(os.Stderr, "error: parse pr list: %v\n", err)
		os.Exit(1)
	}

	prs := make([]tui.PRListItem, 0, len(raw))
	for _, r := range raw {
		prs = append(prs, tui.PRListItem{
			Number:    r.Number,
			Title:     r.Title,
			Author:    r.Author.Login,
			BaseRef:   r.BaseRefName,
			HeadRef:   r.HeadRefName,
			IsDraft:   r.IsDraft,
			UpdatedAt: r.UpdatedAt,
		})
	}

	picker := tui.NewPRList(prs)
	p := tea.NewProgram(picker, tea.WithAltScreen())
	m, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "picker error: %v\n", err)
		os.Exit(1)
	}
	selected := m.(tui.PRList).Selected()
	if selected == 0 {
		return // cancelled
	}
	clearScreen()
	cmdDiffPR(cfg, repoRoot, fmt.Sprintf("%d", selected), plain, false)
}

// cmdDiffPR shows the diff for any open PR (yours or a colleague's). Uses
// `gh pr diff` so no local checkout is needed. Renders in the same TUI as
// the local-branch view, with PR metadata in the header instead of "vs branch".
func cmdDiffPR(cfg config.Config, repoRoot, prNum string, plain, sinceReview bool) {
	if repoRoot == "" {
		fmt.Fprintln(os.Stderr, "error: not inside a git repository (gh needs repo context)")
		os.Exit(1)
	}

	// Fetch PR metadata so the header shows useful context.
	meta, err := fetchPRMeta(repoRoot, prNum)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	var (
		diffOut    []byte
		myComments []tui.ExistingComment
	)
	if sinceReview {
		// Diff from the commit your latest review was stamped on → HEAD, so your
		// (now "outdated") comments line up with the code you reviewed, and the
		// added lines below show how each was addressed.
		out, comments, baseLabel, rerr := reviewSinceDiff(repoRoot, prNum)
		if rerr != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", rerr)
			os.Exit(1)
		}
		diffOut = out
		myComments = comments
		meta.BaseRef = baseLabel
	} else {
		out, derr := exec.Command("gh", "pr", "diff", prNum).Output()
		if derr != nil {
			fmt.Fprintf(os.Stderr, "error: gh pr diff failed: %v\n", derr)
			os.Exit(1)
		}
		diffOut = out
	}
	files, perFileDiff := splitDiffByFile(string(diffOut))

	if plain {
		printDiffPlain(meta.BaseRef, meta.Commits, files, "")
		return
	}

	dv := tui.NewPRDiffView(repoRoot, meta, files, perFileDiff)
	dv = dv.WithExistingComments(myComments).WithSplit(sinceReview)
	p := tea.NewProgram(dv, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "diff TUI error: %v\n", err)
		os.Exit(1)
	}
}

// reviewSinceDiff resolves the reviewer's latest review-stamp commit on the PR
// and returns the unified diff reviewSHA..HEAD plus the reviewer's own review
// comments (anchored to their original lines). Falls back to base...HEAD with a
// note if the user has no review on the PR.
func reviewSinceDiff(repoRoot, prNum string) (diff []byte, comments []tui.ExistingComment, baseLabel string, err error) {
	nwo, err := repoNWO(repoRoot)
	if err != nil {
		return nil, nil, "", err
	}
	login, err := ghLogin()
	if err != nil {
		return nil, nil, "", err
	}
	reviewSHA := latestReviewSHA(nwo, prNum, login)
	if reviewSHA == "" {
		// No review by me — fall back to the full PR diff.
		out, derr := exec.Command("gh", "pr", "diff", prNum).Output()
		if derr != nil {
			return nil, nil, "", fmt.Errorf("gh pr diff: %w", derr)
		}
		fmt.Fprintln(os.Stderr, "note: no review by you on this PR — showing full PR diff")
		return out, nil, "base (no review found)", nil
	}
	headSHA, err := prHeadSHA(prNum)
	if err != nil {
		return nil, nil, "", err
	}
	// Raw unified diff via the compare API (same format as `gh pr diff`).
	out, err := exec.Command("gh", "api",
		fmt.Sprintf("repos/%s/compare/%s...%s", nwo, reviewSHA, headSHA),
		"-H", "Accept: application/vnd.github.diff").Output()
	if err != nil {
		return nil, nil, "", fmt.Errorf("gh api compare: %w", err)
	}
	comments = myReviewComments(nwo, prNum, login)
	return out, comments, "your review @ " + short7(reviewSHA), nil
}

func short7(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

// ghLogin returns the authenticated GitHub username.
func ghLogin() (string, error) {
	out, err := exec.Command("gh", "api", "user", "-q", ".login").Output()
	if err != nil {
		return "", fmt.Errorf("gh api user: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// repoNWO returns the "owner/repo" for the current repo.
func repoNWO(repoRoot string) (string, error) {
	cmd := exec.Command("gh", "repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("gh repo view: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// latestReviewSHA returns the commit_id of login's most-recent review on the PR,
// or "" if none.
func latestReviewSHA(nwo, prNum, login string) string {
	out, err := exec.Command("gh", "api", "--paginate",
		fmt.Sprintf("repos/%s/pulls/%s/reviews", nwo, prNum)).Output()
	if err != nil {
		return ""
	}
	var reviews []struct {
		User        struct{ Login string } `json:"user"`
		SubmittedAt string                 `json:"submitted_at"`
		CommitID    string                 `json:"commit_id"`
	}
	if json.Unmarshal(out, &reviews) != nil {
		return ""
	}
	best, bestAt := "", ""
	for _, r := range reviews {
		if r.User.Login != login || r.CommitID == "" {
			continue
		}
		if r.SubmittedAt >= bestAt { // RFC3339 sorts lexicographically
			bestAt = r.SubmittedAt
			best = r.CommitID
		}
	}
	return best
}

// prHeadSHA returns the PR branch's current HEAD commit SHA.
func prHeadSHA(prNum string) (string, error) {
	out, err := exec.Command("gh", "pr", "view", prNum, "--json", "headRefOid", "-q", ".headRefOid").Output()
	if err != nil {
		return "", fmt.Errorf("gh pr view headRefOid: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// myReviewComments returns login's inline review comments on the PR, anchored to
// the line they were originally written on (valid even when GitHub marks them
// outdated).
func myReviewComments(nwo, prNum, login string) []tui.ExistingComment {
	out, err := exec.Command("gh", "api", "--paginate",
		fmt.Sprintf("repos/%s/pulls/%s/comments", nwo, prNum)).Output()
	if err != nil {
		return nil
	}
	var raw []struct {
		User         struct{ Login string } `json:"user"`
		Path         string                 `json:"path"`
		OriginalLine int                    `json:"original_line"`
		Line         int                    `json:"line"`
		Body         string                 `json:"body"`
		InReplyTo    int64                  `json:"in_reply_to_id"`
	}
	if json.Unmarshal(out, &raw) != nil {
		return nil
	}
	var cs []tui.ExistingComment
	for _, c := range raw {
		if c.User.Login != login {
			continue
		}
		ln := c.OriginalLine
		if ln == 0 {
			ln = c.Line
		}
		cs = append(cs, tui.ExistingComment{
			Path:     c.Path,
			Line:     ln,
			Body:     c.Body,
			Outdated: c.Line == 0,
			IsReply:  c.InReplyTo != 0,
		})
	}
	return cs
}

// fetchPRMeta pulls PR metadata for the header.
func fetchPRMeta(repoRoot, prNum string) (tui.PRMeta, error) {
	out, err := exec.Command("gh", "pr", "view", prNum,
		"--json", "number,title,author,baseRefName,headRefName,commits").Output()
	if err != nil {
		return tui.PRMeta{}, fmt.Errorf("gh pr view: %w", err)
	}
	var raw struct {
		Number      int                    `json:"number"`
		Title       string                 `json:"title"`
		Author      struct{ Login string } `json:"author"`
		BaseRefName string                 `json:"baseRefName"`
		HeadRefName string                 `json:"headRefName"`
		Commits     []struct {
			OID             string `json:"oid"`
			MessageHeadline string `json:"messageHeadline"`
			Authors         []struct {
				Login string `json:"login"`
				Name  string `json:"name"`
			} `json:"authors"`
		} `json:"commits"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return tui.PRMeta{}, fmt.Errorf("parse pr view: %w", err)
	}
	commits := make([]tui.PRCommit, 0, len(raw.Commits))
	for _, c := range raw.Commits {
		author := ""
		if len(c.Authors) > 0 {
			author = c.Authors[0].Login
			if author == "" {
				author = c.Authors[0].Name
			}
		}
		commits = append(commits, tui.PRCommit{
			SHA:     c.OID,
			Subject: c.MessageHeadline,
			Author:  author,
		})
	}
	return tui.PRMeta{
		Number:     raw.Number,
		Title:      raw.Title,
		Author:     raw.Author.Login,
		BaseRef:    raw.BaseRefName,
		HeadRef:    raw.HeadRefName,
		Commits:    len(raw.Commits),
		CommitList: commits,
	}, nil
}

// splitDiffByFile takes one big unified diff and produces:
//   - a DiffFile entry per file (path + added/removed line counts)
//   - a map from file path → that file's slice of raw diff text
func splitDiffByFile(diff string) ([]tui.DiffFile, map[string]string) {
	files := []tui.DiffFile{}
	perFile := map[string]string{}

	lines := strings.Split(diff, "\n")
	var (
		cur     strings.Builder
		curPath string
		added   int
		removed int
	)
	flush := func() {
		if curPath == "" {
			return
		}
		files = append(files, tui.DiffFile{Path: curPath, Added: added, Removed: removed})
		perFile[curPath] = cur.String()
		cur.Reset()
		added, removed = 0, 0
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git ") {
			flush()
			// "diff --git a/<path> b/<path>" — take the b/ side (post-image)
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				curPath = strings.TrimPrefix(parts[3], "b/")
			}
		}
		if curPath != "" {
			cur.WriteString(line)
			cur.WriteByte('\n')
			switch {
			case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
				added++
			case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
				removed++
			}
		}
	}
	flush()
	return files, perFile
}

// cmdDiff shows what's about to be shipped: current branch vs pr_base.
// TUI by default; plain text mode behind --plain for piping / scripts.
func cmdDiff(cfg config.Config, repoRoot string, plain bool, baseOverride string, working bool) {
	if repoRoot == "" {
		fmt.Fprintln(os.Stderr, "error: not inside a git repository")
		os.Exit(1)
	}

	// `-w`/`--working` → current uncommitted changes (staged + unstaged vs HEAD).
	if working {
		numstat, _ := gitOutput(repoRoot, "git", "diff", "--numstat", "HEAD")
		files := parseNumstat(numstat)
		if len(files) == 0 {
			fmt.Println("No uncommitted changes.")
			return
		}
		if plain {
			printDiffPlain("HEAD (uncommitted)", 0, files, "")
			return
		}
		dv := tui.NewDiffView(repoRoot, "HEAD", 0, files, "").WithWorkingTree()
		p := tea.NewProgram(dv, tea.WithAltScreen(), tea.WithMouseCellMotion())
		if _, err := p.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "diff TUI error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	branch := strings.TrimSpace(currentBranch(repoRoot))
	if branch == "" {
		fmt.Fprintln(os.Stderr, "error: could not determine current branch")
		os.Exit(1)
	}

	// Resolve the diff base. `--base <ref>` wins outright (lets the user point
	// at any local or remote ref — `master`, `origin/HEAD`, `feature/foo`,
	// `@{u}`, etc.). Otherwise default to the branch's fork base, so the diff is
	// the whole branch: everything from where it forked to HEAD, committed,
	// pushed or not.
	var ref, target string
	if baseOverride != "" {
		ref = baseOverride
		// For display purposes, strip a leading `origin/` so the header shows
		// just the branch name.
		target = strings.TrimPrefix(baseOverride, "origin/")
	} else {
		target = resolveBranchBase(cfg, repoRoot, "")
		if target == "" {
			fmt.Fprintln(os.Stderr, "error: no branch_base/pr_base configured (use --base <ref> to override)")
			os.Exit(1)
		}
		// The fork commit lives on the local base ref; fall back to origin/ if
		// the base branch isn't checked out locally.
		ref = target
		if !refExists(repoRoot, ref) && refExists(repoRoot, "origin/"+target) {
			ref = "origin/" + target
		}
	}
	if branch == target {
		fmt.Printf("On %s — nothing to compare.\n", target)
		return
	}
	// In dual-base workflows (branch from master, PR to staging) the branch
	// will never be a descendant of the PR target — that's expected, not a
	// pollution warning. Skip the ancestor check.
	warn := ""

	commitCountRaw, _ := gitOutput(repoRoot, "git", "rev-list", "--count", ref+"..HEAD")
	commitCount, _ := strconv.Atoi(strings.TrimSpace(commitCountRaw))

	numstat, _ := gitOutput(repoRoot, "git", "diff", "--numstat", ref+"...HEAD")
	files := parseNumstat(numstat)

	if len(files) == 0 {
		fmt.Printf("No committed changes vs %s.\n", target)
		if wip, _ := gitOutput(repoRoot, "git", "diff", "--numstat", "HEAD"); strings.TrimSpace(wip) != "" {
			fmt.Println("(you have uncommitted changes — see them with `norn diff -w`)")
		}
		return
	}

	if plain {
		printDiffPlain(target, commitCount, files, warn)
		return
	}

	dv := tui.NewDiffView(repoRoot, ref, commitCount, files, warn)
	p := tea.NewProgram(dv, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "diff TUI error: %v\n", err)
		os.Exit(1)
	}
}

func parseNumstat(numstat string) []tui.DiffFile {
	var out []tui.DiffFile
	for _, line := range strings.Split(numstat, "\n") {
		parts := strings.Fields(line)
		if len(parts) < 3 {
			continue
		}
		added, _ := strconv.Atoi(parts[0])
		removed, _ := strconv.Atoi(parts[1])
		out = append(out, tui.DiffFile{Path: parts[2], Added: added, Removed: removed})
	}
	return out
}

func printDiffPlain(target string, commits int, files []tui.DiffFile, warn string) {
	if warn != "" {
		fmt.Println(warnStyle(warn))
		fmt.Println()
	}
	if len(files) == 0 {
		fmt.Printf("No diff vs %s.\n", target)
		return
	}
	added, removed := 0, 0
	for _, f := range files {
		added += f.Added
		removed += f.Removed
	}
	fmt.Printf("%d files changed, +%d / -%d · base: %s · %d commit(s)\n\n", len(files), added, removed, target, commits)

	groups := groupTUI(files)
	for _, g := range groups {
		fmt.Printf("%-40s %4d file(s)   +%-6d / -%d\n", g.dir+"/", len(g.files), g.added, g.removed)
		for _, f := range g.files {
			fmt.Printf("  %-50s +%-6d / -%d\n", f.relPath, f.added, f.removed)
		}
		fmt.Println()
	}
}

func groupTUI(files []tui.DiffFile) []diffGroup {
	byDir := map[string]*diffGroup{}
	var order []string
	for _, f := range files {
		dir := topTwoDirs(f.Path)
		if _, ok := byDir[dir]; !ok {
			byDir[dir] = &diffGroup{dir: dir}
			order = append(order, dir)
		}
		g := byDir[dir]
		g.files = append(g.files, diffFile{relPath: relTo(f.Path, dir), added: f.Added, removed: f.Removed})
		g.added += f.Added
		g.removed += f.Removed
	}
	out := make([]diffGroup, 0, len(order))
	for _, d := range order {
		g := byDir[d]
		sortFilesByChurn(g.files)
		out = append(out, *g)
	}
	return out
}

func resolvePRBase(cfg config.Config) string {
	if cfg.PRBase != "" {
		return cfg.PRBase
	}
	if len(cfg.BaseBranches) > 0 {
		return cfg.BaseBranches[0]
	}
	return ""
}

// resolveBranchBase picks the branch new worktrees fork from. Distinct from
// resolvePRBase — many teams branch from production (master) but merge to
// staging (staging).
func resolveBranchBase(cfg config.Config, repoRoot, override string) string {
	switch {
	case override != "":
		return override
	case cfg.BranchBase != "":
		return cfg.BranchBase
	case cfg.PRBase != "":
		return cfg.PRBase
	}
	if d := detectDefaultBranch(repoRoot); d != "" {
		return d
	}
	return "main"
}

// resolvePRTarget picks the PR target for a given branch name. Hotfix branches
// (prefix configurable, default "hotfix/") route to HotfixTarget when set;
// everything else uses PRBase.
func resolvePRTarget(cfg config.Config, branchName string) string {
	prefix := cfg.HotfixPrefix
	if prefix == "" {
		prefix = "hotfix/"
	}
	if cfg.HotfixTarget != "" && strings.HasPrefix(branchName, prefix) {
		return cfg.HotfixTarget
	}
	return resolvePRBase(cfg)
}

// refExists reports whether a git ref (branch, remote-tracking, sha) resolves.
func refExists(repoRoot, ref string) bool {
	_, err := gitOutput(repoRoot, "git", "rev-parse", "--verify", "--quiet", ref)
	return err == nil
}

func gitOutput(dir string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

func warnStyle(s string) string {
	// Yellow ANSI in case terminal supports color; falls back to plain text fine.
	return "\033[33m" + s + "\033[0m"
}

type diffFile struct {
	relPath string
	added   int
	removed int
}

type diffGroup struct {
	dir     string
	files   []diffFile
	added   int
	removed int
}

func topTwoDirs(p string) string {
	parts := strings.Split(p, "/")
	switch len(parts) {
	case 1:
		return "(root)"
	case 2:
		return parts[0]
	default:
		return parts[0] + "/" + parts[1]
	}
}

func relTo(p, dir string) string {
	if dir == "(root)" {
		return p
	}
	if strings.HasPrefix(p, dir+"/") {
		return p[len(dir)+1:]
	}
	return p
}

func sortFilesByChurn(fs []diffFile) {
	sort.Slice(fs, func(i, j int) bool {
		return (fs[i].added + fs[i].removed) > (fs[j].added + fs[j].removed)
	})
}

// cmdInit scaffolds a project config for the current repo. Detects stack,
// suggests verify commands + base branches. Refuses to overwrite an existing
// file — tells you where it lives so you can open it yourself.
func cmdInit(repoRoot string) {
	if repoRoot == "" {
		fmt.Fprintln(os.Stderr, "error: not inside a git repository")
		os.Exit(1)
	}
	home, _ := os.UserHomeDir()
	repoName := originRepoName(repoRoot)
	target := filepath.Join(home, ".config", "work", "projects", repoName+".yaml")

	if _, err := os.Stat(target); err == nil {
		fmt.Printf("Project config already exists:\n  %s\n\nEdit it directly, or remove it first to regenerate.\n", short(target, home))
		return
	}

	stack := detectStack(repoRoot)
	baseBranch := detectDefaultBranch(repoRoot)
	verify := suggestVerify(stack)
	format := suggestFormat(stack)

	body := renderInitTemplate(repoName, baseBranch, stack, verify, format)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(target, []byte(body), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ Project config created:\n  %s\n\nDetected stack: %s\nBase branch:    %s\n\nNext: edit it (forbid/format/review/docs), then `work --doctor` to validate.\n", short(target, home), stack, baseBranch)
}

// detectStack returns a short tag for the repo's primary language ecosystem.
func detectStack(root string) string {
	exists := func(rel string) bool {
		_, err := os.Stat(filepath.Join(root, rel))
		return err == nil
	}
	switch {
	case exists("pnpm-lock.yaml"):
		return "pnpm"
	case exists("package-lock.json"):
		return "npm"
	case exists("yarn.lock"):
		return "yarn"
	case exists("go.mod"):
		return "go"
	case exists("pyproject.toml"):
		return "python"
	case exists("Cargo.toml"):
		return "rust"
	default:
		return "unknown"
	}
}

// detectDefaultBranch asks git for the remote's HEAD branch; falls back to
// any common default.
func detectDefaultBranch(root string) string {
	cmd := exec.Command("git", "-C", root, "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	out, err := cmd.Output()
	if err == nil {
		b := strings.TrimSpace(string(out))
		if idx := strings.LastIndex(b, "/"); idx >= 0 {
			return b[idx+1:]
		}
		return b
	}
	for _, candidate := range []string{"main", "master", "trunk"} {
		c := exec.Command("git", "-C", root, "rev-parse", "--verify", candidate)
		if c.Run() == nil {
			return candidate
		}
	}
	return "main"
}

func suggestVerify(stack string) []string {
	switch stack {
	case "pnpm":
		return []string{"pnpm check-types", "pnpm lint"}
	case "npm":
		return []string{"npm run check-types", "npm run lint"}
	case "yarn":
		return []string{"yarn check-types", "yarn lint"}
	case "go":
		return []string{"go build ./...", "go vet ./..."}
	case "python":
		return []string{"ruff check ."}
	case "rust":
		return []string{"cargo check", "cargo clippy"}
	default:
		return nil
	}
}

func suggestFormat(stack string) string {
	switch stack {
	case "pnpm", "npm", "yarn":
		return `  - { ext: [ts, tsx, js, jsx, json, html, scss, css, md], cmd: 'pnpm prettier --write --log-level warn' }`
	case "go":
		return `  - { ext: [go], cmd: 'gofmt -w' }`
	case "python":
		return `  - { ext: [py], cmd: 'ruff format' }`
	case "rust":
		return `  - { ext: [rs], cmd: 'rustfmt' }`
	}
	return ""
}

func renderInitTemplate(repo, base, stack string, verify []string, formatLine string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s — norn project config\n", repo)
	fmt.Fprintf(&b, "# stack detected: %s · default branch: %s\n", stack, base)
	b.WriteString("# Edit to taste, then `norn doctor` to validate.\n\n")

	if len(verify) > 0 {
		b.WriteString("# Commands /precheck runs before opening a PR.\nverify:\n")
		for _, v := range verify {
			fmt.Fprintf(&b, "  - %s\n", v)
		}
	} else {
		b.WriteString("# Commands /precheck runs before opening a PR.\nverify: []\n")
	}
	b.WriteString("\n")

	fmt.Fprintf(&b, "base_branches: [%s]\n\n", base)

	b.WriteString("# Task picker (New tab → T). github uses gh; clickup needs a token (`norn auth`).\n# tasks:\n#   provider: github   # github | clickup\n\n")

	b.WriteString("# Patch-time rejections. Each rule: { match: <regex>, reason: <str>, glob?: <fnmatch>, severity?: 'block'|'warn' }\nforbid: []\n\n")

	b.WriteString("# Post-write formatters. Hooks run `cmd <file>` after every Edit/Write.\n")
	if formatLine != "" {
		b.WriteString("format:\n")
		b.WriteString(formatLine + "\n")
	} else {
		b.WriteString("format: []\n")
	}
	b.WriteString("\n")

	b.WriteString("# Authoritative files /pr-judge reads when reviewing PRs.\nreview: []\n\n")

	b.WriteString("# Named pointers at canonical docs. Pull with `norn --refresh-docs`.\n")
	b.WriteString("# Example:\n#   pr_guidelines: ./docs/pr-guidelines.md\ndocs: {}\n")

	return b.String()
}

func short(p, home string) string {
	if home != "" && strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}

// cmdDoctor checks the full work + hook + skill installation. Reports per-check
// status with a fix hint. Exit code = number of failures.
func cmdDoctor(cfg config.Config, repoRoot string) {
	home, _ := os.UserHomeDir()
	checks := []doctorCheck{
		checkBinary("git", true, "required for everything norn does"),
		checkBinary("gh", false, "needed for PR features (norn diff <pr#>, --since-review)"),
		checkBinary("claude", false, "needed to launch sessions + AI features (summarize, AI naming)"),
		checkGlobalConfig(home),
		checkProjectConfigs(home),
		checkDocsPaths(home),
		checkStateFile(home),
		checkActiveRepo(cfg, repoRoot),
	}
	render := func(c doctorCheck) {
		mark := "✓"
		if c.warn {
			mark = "⚠"
		}
		if c.fail {
			mark = "✗"
		}
		fmt.Printf("  %s %s\n", mark, c.name)
		if c.detail != "" {
			fmt.Printf("      %s\n", c.detail)
		}
		if c.fix != "" && (c.fail || c.warn) {
			fmt.Printf("      fix: %s\n", c.fix)
		}
	}
	fmt.Println("norn doctor")
	fmt.Println()
	fails, warns := 0, 0
	for _, c := range checks {
		render(c)
		if c.fail {
			fails++
		} else if c.warn {
			warns++
		}
	}
	fmt.Println()
	switch {
	case fails > 0:
		fmt.Printf("%d failed · %d warned · system not fully healthy\n", fails, warns)
		os.Exit(fails)
	case warns > 0:
		fmt.Printf("0 failed · %d warned · system usable, see notes above\n", warns)
	default:
		fmt.Println("0 failed · 0 warned · system healthy")
	}
}

type doctorCheck struct {
	name   string
	detail string
	fix    string
	warn   bool
	fail   bool
}

// checkBinary reports whether a CLI is on PATH. Required-missing fails; an
// optional one only warns, with a note on what degrades without it.
func checkBinary(name string, required bool, note string) doctorCheck {
	if _, err := exec.LookPath(name); err != nil {
		c := doctorCheck{name: name + " in PATH", detail: note, fix: "install " + name}
		if required {
			c.fail = true
		} else {
			c.warn = true
		}
		return c
	}
	return doctorCheck{name: name + " in PATH"}
}

func checkGlobalConfig(home string) doctorCheck {
	p := filepath.Join(home, ".config", "work", "config.yaml")
	data, err := os.ReadFile(p)
	if err != nil {
		return doctorCheck{
			name:   "global config parses",
			detail: p + " missing",
			fix:    "create with worktree_dir + user defaults",
			warn:   true,
		}
	}
	var c config.Config
	if err := config.UnmarshalYAML(data, &c); err != nil {
		return doctorCheck{name: "global config parses", detail: err.Error(), fail: true}
	}
	return doctorCheck{name: "global config parses"}
}

func checkProjectConfigs(home string) doctorCheck {
	dir := filepath.Join(home, ".config", "work", "projects")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return doctorCheck{name: "project configs parse", detail: "no projects dir yet", warn: true, fix: "`norn init` inside any repo"}
	}
	var bad []string
	count := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		count++
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			bad = append(bad, e.Name())
			continue
		}
		var c config.Config
		if err := config.UnmarshalYAML(data, &c); err != nil {
			bad = append(bad, e.Name()+": "+err.Error())
		}
	}
	if len(bad) > 0 {
		return doctorCheck{
			name:   fmt.Sprintf("project configs parse (%d)", count),
			detail: strings.Join(bad, "; "),
			fail:   true,
		}
	}
	if count == 0 {
		return doctorCheck{name: "project configs parse", detail: "no project configs yet", warn: true, fix: "`norn init` inside any repo"}
	}
	return doctorCheck{name: fmt.Sprintf("project configs parse (%d)", count)}
}

func checkDocsPaths(home string) doctorCheck {
	dir := filepath.Join(home, ".config", "work", "projects")
	entries, _ := os.ReadDir(dir)
	var missing []string
	total := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var c config.Config
		if err := config.UnmarshalYAML(data, &c); err != nil {
			continue
		}
		for key, raw := range c.Docs {
			total++
			p := expandHome(raw)
			if _, err := os.Stat(p); err != nil {
				missing = append(missing, fmt.Sprintf("%s:%s", e.Name(), key))
			}
		}
	}
	if total == 0 {
		return doctorCheck{name: "docs paths resolve", detail: "no docs configured"}
	}
	if len(missing) > 0 {
		return doctorCheck{
			name:   fmt.Sprintf("docs paths resolve (%d)", total),
			detail: "missing: " + strings.Join(missing, ", "),
			fix:    "clone the doc repos or run `work --refresh-docs`",
			warn:   true,
		}
	}
	return doctorCheck{name: fmt.Sprintf("docs paths resolve (%d)", total)}
}

func checkStateFile(home string) doctorCheck {
	p := filepath.Join(home, ".local", "state", "work", "sessions.json")
	if _, err := os.Stat(p); err != nil {
		return doctorCheck{name: "session state file readable", detail: "not created yet (normal on fresh install)", warn: false}
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return doctorCheck{name: "session state file readable", detail: err.Error(), fail: true}
	}
	if len(data) > 0 {
		var probe map[string]any
		if err := json.Unmarshal(data, &probe); err != nil {
			return doctorCheck{name: "session state file readable", detail: "invalid JSON: " + err.Error(), fail: true, fix: "delete " + p}
		}
	}
	return doctorCheck{name: "session state file readable"}
}

func checkActiveRepo(cfg config.Config, repoRoot string) doctorCheck {
	if repoRoot == "" {
		return doctorCheck{name: "current dir: not in a git repo (skipping)"}
	}
	home, _ := os.UserHomeDir()
	repoName := originRepoName(repoRoot)
	p := filepath.Join(home, ".config", "work", "projects", repoName+".yaml")
	if _, err := os.Stat(p); err != nil {
		return doctorCheck{
			name: "current repo: " + repoName,
			fix:  "`norn init` to scaffold a project config",
			warn: true,
		}
	}
	return doctorCheck{name: "current repo: " + repoName + " (has project config)"}
}

// cmdRefreshDocs scans every project config under ~/.config/work/projects/,
// collects unique git repos referenced by any `docs:` value, and runs
// `git pull --ff-only` against each. Reports per-repo outcome.
func cmdRefreshDocs() {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".config", "work", "projects")

	entries, err := os.ReadDir(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "no project configs at %s\n", dir)
		return
	}

	seen := map[string]bool{}
	var repos []string

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var c config.Config
		// best-effort parse; ignore errors per file
		_ = yamlUnmarshal(data, &c)
		for _, raw := range c.Docs {
			p := expandHome(raw)
			root, err := findGitToplevel(p)
			if err != nil || root == "" {
				continue
			}
			if seen[root] {
				continue
			}
			seen[root] = true
			repos = append(repos, root)
		}
	}

	if len(repos) == 0 {
		fmt.Println("No doc repos found in any project config.")
		return
	}

	fmt.Printf("Refreshing %d doc repo(s):\n", len(repos))
	for _, r := range repos {
		short := r
		if home != "" && strings.HasPrefix(r, home) {
			short = "~" + r[len(home):]
		}
		cmd := exec.Command("git", "-C", r, "pull", "--ff-only")
		out, err := cmd.CombinedOutput()
		msg := strings.TrimSpace(string(out))
		switch {
		case err != nil:
			fmt.Printf("  ✗ %s\n     %s\n", short, firstLine(msg))
		case strings.Contains(msg, "Already up to date"):
			fmt.Printf("  · %s  (already up to date)\n", short)
		default:
			fmt.Printf("  ✓ %s  (updated)\n", short)
		}
	}
}

func yamlUnmarshal(data []byte, v any) error {
	return config.UnmarshalYAML(data, v)
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}

func findGitToplevel(p string) (string, error) {
	// Walk up until we hit a .git dir or filesystem root.
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	// If p is a file, start from its directory.
	if info, err := os.Stat(abs); err == nil && !info.IsDir() {
		abs = filepath.Dir(abs)
	}
	for {
		if _, err := os.Stat(filepath.Join(abs, ".git")); err == nil {
			return abs, nil
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", fmt.Errorf("no .git found above %s", p)
		}
		abs = parent
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func cmdActivityTick(repoRoot string) {
	if repoRoot == "" {
		return
	}
	// Only real worktrees are threads; never record the main checkout. This is
	// where the log used to balloon — every branch switch in the main repo
	// inserted a fresh row.
	if git.CheckoutClass(repoRoot) != "worktree" {
		return
	}
	branch := currentBranch(repoRoot)
	if branch == "" { // detached or error
		return
	}
	repo := originRepoName(repoRoot)

	store, err := state.Load()
	if err != nil {
		return
	}
	// Key by path: a thread is a worktree, not a branch. Branch switches update
	// the existing row rather than spawning a new one.
	if existing := store.FindByPath(repoRoot); existing != nil {
		existing.Branch = branch
		existing.ID = state.MakeID(repo, branch)
		existing.LastActivityAt = time.Now()
		if existing.ClickUpID == "" {
			existing.ClickUpID = git.ClickUpID(branch)
		}
	} else {
		kind := "task"
		if strings.HasPrefix(branch, "review/") {
			kind = "review"
		}
		store.UpsertByPath(state.Session{
			ID:             state.MakeID(repo, branch),
			Repo:           repo,
			Branch:         branch,
			Kind:           kind,
			Path:           repoRoot,
			ClickUpID:      git.ClickUpID(branch),
			Status:         state.StatusActive,
			StartedAt:      time.Now(),
			LastActivityAt: time.Now(),
		})
	}
	_ = store.Save()
}

func currentBranch(repoRoot string) string {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	b := strings.TrimSpace(string(out))
	if b == "HEAD" { // detached — not a real thread
		return ""
	}
	return b
}

// upsertSession records a newly created worktree to the session store so the
// dashboard sees it immediately, before any activity-tick fires.
func upsertSession(repoRoot, kind, branch, wtPath, hint string) {
	if branch == "" || branch == "HEAD" {
		return
	}
	repo := originRepoName(repoRoot)
	id := state.MakeID(repo, branch)
	store, err := state.Load()
	if err != nil {
		return
	}
	// Prefer the branch (reliably carries #<id> after naming); fall back to the hint.
	clickup := git.ClickUpID(branch)
	if clickup == "" {
		clickup = git.ClickUpID(hint)
	}
	store.UpsertByPath(state.Session{
		ID:             id,
		Repo:           repo,
		Branch:         branch,
		Kind:           kind,
		Path:           wtPath,
		ClickUpID:      clickup,
		Status:         state.StatusActive,
		StartedAt:      time.Now(),
		LastActivityAt: time.Now(),
	})
	_ = store.Save()
}

// posixShellInit / fishShellInit are the shell wrapper that lets norn cd its
// parent shell. A child process can't chdir its parent, so the shell must do it
// by reading the cd-target file norn writes. Keeping this in the binary (via
// `eval "$(norn shell-init)"`) means it never drifts out of sync like a
// hand-copied function does.
const posixShellInit = `norn() {
  command norn "$@"
  local code=$?
  local t="$HOME/.cache/work/cd-target-$$"
  if [ -f "$t" ]; then
    local d; d=$(cat "$t"); rm -f "$t"
    [ -d "$d" ] && cd "$d"
  fi
  return $code
}
`

const fishShellInit = `function norn
  command norn $argv
  set -l code $status
  set -l t "$HOME/.cache/work/cd-target-"$fish_pid
  if test -f "$t"
    set -l d (cat "$t"); rm -f "$t"
    test -d "$d"; and cd "$d"
  end
  return $code
end
`

// cmdShellInit prints the shell wrapper for the given shell (auto-detected from
// $SHELL when empty). Meant for `eval "$(norn shell-init zsh)"` in a shell rc.
func cmdShellInit(shell string) {
	if shell == "" {
		shell = filepath.Base(os.Getenv("SHELL"))
	}
	switch shell {
	case "fish":
		fmt.Print(fishShellInit)
	default: // zsh, bash, sh
		fmt.Print(posixShellInit)
	}
}

// cmdAuth connects an integration via an interactive huh form. `norn auth
// <name>` jumps straight in; bare `norn auth` shows a provider picker.
func cmdAuth(args []string) {
	provider := ""
	if len(args) > 0 {
		provider = args[0]
	} else {
		err := huh.NewSelect[string]().
			Title("Connect an integration").
			Description("Task sources for the New tab's picker").
			Options(
				huh.NewOption("ClickUp   ·  "+clickupStatus(), "clickup"),
				huh.NewOption("GitHub    ·  "+githubStatus(), "github"),
			).
			Value(&provider).
			WithTheme(tui.HuhTheme()).
			Run()
		if err != nil {
			return // aborted
		}
	}

	switch provider {
	case "clickup":
		clickupLogin()
	case "github":
		fmt.Println("GitHub auth is handled by the gh CLI — run `gh auth login`.")
	default:
		fmt.Fprintf(os.Stderr, "unknown integration %q (try: norn auth)\n", provider)
		os.Exit(1)
	}
}

func clickupStatus() string {
	if os.Getenv("CLICKUP_TOKEN") != "" {
		return "token in $CLICKUP_TOKEN"
	}
	if cfg, err := config.Load(""); err == nil && cfg.ClickUp != nil && cfg.ClickUp.Token != "" {
		return "connected"
	}
	return "not connected"
}

func githubStatus() string {
	if exec.Command("gh", "auth", "status").Run() == nil {
		return "connected via gh"
	}
	return "run `gh auth login`"
}

// nidOptions turns tracker items into huh select options.
func nidOptions(items []task.NamedID) []huh.Option[string] {
	opts := make([]huh.Option[string], 0, len(items))
	for _, it := range items {
		opts = append(opts, huh.NewOption(it.Name, it.ID))
	}
	return opts
}

// clickupLogin walks the user through token → workspace → space → list, then
// saves the token + scope to global config. Each step is an interactive form.
func clickupLogin() {
	ctx := context.Background()
	var token, name string
	err := huh.NewInput().
		Title("ClickUp personal token").
		Description("ClickUp → Settings → Apps (starts with pk_)").
		EchoMode(huh.EchoModePassword).
		Value(&token).
		Validate(func(s string) error {
			s = strings.TrimSpace(s)
			if s == "" {
				return errors.New("token required")
			}
			n, err := task.ClickUpUser(ctx, s)
			if err != nil {
				return err // shown inline; blocks submit until the token is valid
			}
			name = n
			return nil
		}).
		WithTheme(tui.HuhTheme()).
		Run()
	if err != nil {
		return // aborted
	}
	token = strings.TrimSpace(token)

	// Workspace (auto if there's only one).
	team, space, list := "", "", ""
	if teams, err := task.ClickUpTeams(ctx, token); err == nil && len(teams) > 0 {
		team = teams[0].ID
		if len(teams) > 1 {
			if err := huh.NewSelect[string]().Title("Workspace").Options(nidOptions(teams)...).Value(&team).WithTheme(tui.HuhTheme()).Run(); err != nil {
				return
			}
		}
		// Space (optional — "All spaces" keeps the assigned-tasks default).
		if spaces, err := task.ClickUpSpaces(ctx, token, team); err == nil && len(spaces) > 0 {
			opts := append([]huh.Option[string]{huh.NewOption("All spaces (your assigned tasks)", "")}, nidOptions(spaces)...)
			if err := huh.NewSelect[string]().Title("Space").Options(opts...).Value(&space).WithTheme(tui.HuhTheme()).Run(); err != nil {
				return
			}
			// List within the chosen space (optional).
			if space != "" {
				if lists, err := task.ClickUpLists(ctx, token, space); err == nil && len(lists) > 0 {
					opts := append([]huh.Option[string]{huh.NewOption("All lists in this space", "")}, nidOptions(lists)...)
					if err := huh.NewSelect[string]().Title("List").Options(opts...).Value(&list).WithTheme(tui.HuhTheme()).Run(); err != nil {
						return
					}
				}
			}
		}
	}

	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".config", "work", "config.yaml")
	ed, err := config.OpenEditor(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	ed.SetString([]string{"clickup", "token"}, token)
	ed.SetString([]string{"tasks", "provider"}, "clickup")
	setOrClear(ed, []string{"clickup", "team"}, team)
	setOrClear(ed, []string{"clickup", "space"}, space)
	setOrClear(ed, []string{"clickup", "list"}, list)
	if err := ed.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "error saving config: %v\n", err)
		os.Exit(1)
	}
	scope := "your assigned tasks"
	if list != "" {
		scope = "a specific list"
	} else if space != "" {
		scope = "a space"
	}
	fmt.Printf("✓ Connected to ClickUp as %s (%s) — saved to %s\n", name, scope, path)
	fmt.Println("  New tab → T now lists those tasks.")
}

func setOrClear(ed *config.Editor, keys []string, val string) {
	if val == "" {
		ed.Delete(keys)
		return
	}
	ed.SetString(keys, val)
}

// cmdTemplateEdit opens a template in $EDITOR, seeding a user copy from the
// built-in first (so even the default `task`/`review` can be customized).
func cmdTemplateEdit(name string) {
	path, err := prompt.EnsureUserTemplate(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	cmd := config.EditorCommand(path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "editor: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("saved %s\n", path)
}

func cmdTemplateNew(name string) {
	path, err := prompt.NewTemplate(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("created %s\n", path)
	fmt.Printf("edit it, then use it with: norn \"hint\" --template %s\n", name)
}

func cmdTemplates(cfg config.Config) {
	names := prompt.List()
	if len(names) == 0 {
		fmt.Println("no templates found")
		return
	}
	def := prompt.Resolve(cfg, "task", "") // the default a bare `norn "hint"` uses
	fmt.Println("Templates (* = default for new tasks):")
	for _, n := range names {
		marker := "  "
		if n == def {
			marker = "* "
		}
		fmt.Printf("%s%s\n", marker, n)
	}
	fmt.Println("\nData available in templates:")
	for _, f := range prompt.DataFields() {
		fmt.Printf("  %s\n", f)
	}
	fmt.Println("\nUse: norn \"hint\" --template <name>   ·   norn template new <name> to scaffold")
}

func cmdProjectConfig(cfg config.Config, repoRoot string) {
	// Emit the fully-resolved config as JSON. Used by hooks + skills that need
	// per-project policy (forbid/format/review), verify cmds, clickup lists.
	out := map[string]any{
		"repo_root": repoRoot,
		"config":    cfg,
	}
	if repoRoot != "" {
		out["repo_name"] = originRepoName(repoRoot)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}

// originRepoName returns the upstream repo basename for any directory inside
// a git repo or worktree. Worktrees report the main repo's name, not the
// worktree dir's basename — so per-project state + config stays stable.
func originRepoName(dir string) string {
	if dir == "" {
		return ""
	}
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--git-common-dir")
	out, err := cmd.Output()
	if err != nil {
		return filepath.Base(dir)
	}
	common := strings.TrimSpace(string(out))
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	if abs, err := filepath.EvalSymlinks(common); err == nil {
		common = abs
	}
	return filepath.Base(filepath.Dir(common))
}

func cmdHelp() {
	fmt.Println(`norn · many threads, one tree
git worktrees + Claude Code sessions, woven together.

Usage:
  norn                    Tabbed TUI: Threads · New · Clean · Settings
                          (Tab / 1-4 switch, ? keys, esc back, q quit)
  norn create "hint"      Create task worktree with hint (base: pr_base default)
  norn create             No hint → open the New tab to compose it
  norn create "hint" --from <b>
                          Override base branch for this worktree
  norn create "hint" --template <name>, -t <name>
                          Use a specific prompt template for this worktree
  norn review <pr#>       Check out a PR into a worktree + launch the agent to review it
  norn --clean            Open on the Clean tab
  norn settings           Open on the Settings tab
  norn --cd               Jump into a worktree shell (picker)
  norn --list             List worktrees (git)
  norn --status           Show worktrees with details
  norn --project-config   Print resolved config as JSON
  norn --templates        List prompt templates + the data they can use
  norn template new <name>  Scaffold a user template (from the task template)
  norn template edit [name]  Customize a template in $EDITOR (default: task)
  norn auth [provider]    Connect an integration (ClickUp, …) for the task picker
  norn shell-init [shell]  Print the cd wrapper: eval "$(norn shell-init zsh)"
  norn diff               TUI diff of the whole branch: fork base → HEAD
                          (every commit since the branch was created, pushed or not)
  norn diff -w, --working  Just current uncommitted changes (working tree)
  norn diff --base [ref]  Compare against a specific ref instead of the fork base
                          (origin/HEAD, master, @{u}, another branch, …)
  norn diff <pr#>         TUI diff of any open PR (yours or colleague's)
  norn diff <pr#> --since-review
                          Diff from your last review's commit to HEAD, with your
                          (even "outdated") comments overlaid next to the code
  norn diff --list, -l    Pick an open PR from a list, then view its diff
  norn diff --plain, -p   Plain text diff for piping
  norn init               Scaffold a project config for the current repo
  norn doctor             Diagnose hooks, skills, configs, docs, state
  norn --refresh-docs     git pull every doc repo referenced in any project config
  norn --activity-tick    Bump current session's last_activity_at (called by hook)
  norn --version          Print the version
  norn --help             This help`)
}

func clearScreen() {
	// Home + clear screen + clear scrollback, so the agent spawns on a truly
	// clean terminal with nothing from the norn session left above it.
	fmt.Print("\033[H\033[2J\033[3J")
}
