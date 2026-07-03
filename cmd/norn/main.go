package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sandbye/norn/internal/claude"
	"github.com/sandbye/norn/internal/config"
	"github.com/sandbye/norn/internal/git"
	"github.com/sandbye/norn/internal/prompt"
	"github.com/sandbye/norn/internal/state"
	"github.com/sandbye/norn/internal/tui"
)

func main() {
	repoRoot, _ := git.RepoRoot()
	cfg, err := config.Load(repoRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
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
			return
		case "--activity-tick":
			cmdActivityTick(repoRoot)
			return
		case "--dashboard":
			cmdDashboard(cfg, repoRoot)
			return
		case "--refresh-docs":
			cmdRefreshDocs()
			return
		case "--diff", "diff":
			plain := false
			list := false
			sinceReview := false
			baseFlag := false
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
				case a == "--since-review":
					sinceReview = true
				case a == "--base" || a == "-b":
					baseFlag = true
					// `--base <ref>` — peek next arg (may be omitted → pr_base).
					if i+1 < len(diffArgs) {
						baseOverride = diffArgs[i+1]
						i++
					}
				case strings.HasPrefix(a, "--base="):
					baseFlag = true
					baseOverride = strings.TrimPrefix(a, "--base=")
				case strings.HasPrefix(a, "-b="):
					baseFlag = true
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
			// Bare `work diff` → current uncommitted changes. `--base` (with or
			// without a ref) → compare against a base ref / pr_base.
			cmdDiff(cfg, repoRoot, plain, baseOverride, !baseFlag)
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
		}
	}

	// Determine initial view
	initialView := tui.ViewMenu
	cdMode := false
	switch {
	case len(args) > 0 && args[0] == "--clean":
		initialView = tui.ViewClean
	case len(args) > 0 && args[0] == "--cd":
		initialView = tui.ViewCd
	case len(args) > 0 && (args[0] == "-d" || args[0] == "--dir"):
		// `work -d` — the normal main menu, but enter cd's into the worktree
		// (l launches Claude). Jump-to-dir without leaving the familiar list.
		initialView = tui.ViewMenu
		cdMode = true
	}

	// Direct create: `work "some hint"` or `work --review "hint"`
	if len(args) > 0 && args[0] != "--clean" && args[0] != "--cd" && args[0] != "-d" && args[0] != "--dir" {
		if repoRoot == "" {
			fmt.Fprintln(os.Stderr, "error: not inside a git repository")
			os.Exit(1)
		}
		// Extract `--from <branch>` / `-b <branch>` if present, so the rest
		// can flow into the hint without polluting it.
		baseOverride, rest := extractFromFlag(args)
		kind := "task"
		hint := strings.Join(rest, " ")
		if len(rest) > 0 && rest[0] == "--review" {
			kind = "review"
			hint = strings.Join(rest[1:], " ")
		}
		directCreate(cfg, repoRoot, kind, hint, baseOverride)
		return
	}

	// Bare `work` → the cross-session dashboard (the default view). Scoped to
	// the current repo when inside one, global otherwise. The resume/new/clean
	// menu is reached explicitly: `work -d` (cd-mode), `work --clean`.
	if initialView == tui.ViewMenu && !cdMode {
		cmdDashboard(cfg, repoRoot)
		return
	}
	if repoRoot == "" && initialView != tui.ViewClean {
		fmt.Fprintln(os.Stderr, "error: not inside a git repository")
		os.Exit(1)
	}

	reapStale(repoRoot)

	app := tui.NewAppMode(cfg, repoRoot, initialView, cdMode)
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
		tui.LaunchClaude(result.Path, false)
	case tui.ResultResume:
		upsertSessionFromPath(repoRoot, cfg.WorktreeDir, result.Path)
		writeCdTarget(result.Path)
		clearScreen()
		tui.LaunchClaude(result.Path, true)
	case tui.ResultCd:
		// Parent-shell cd: write the target and exit. The `work()` shell wrapper
		// cd's the *current* shell into it — no nested subshell.
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
	if repoRoot == "" {
		return
	}
	_ = git.PruneWorktrees(repoRoot)
}

// aiResolveBranch is MakeBranch, but when the deterministic name lacks a
// descriptive slug (e.g. created from a bare ClickUp id) it asks Claude for a
// better one. Gated on config + `claude` availability; falls back silently.
func aiResolveBranch(cfg config.Config, repoRoot, kind, hint string) string {
	branch := git.MakeBranch(kind, hint)
	if cfg.AINaming && claude.Available() && git.BranchLacksSlug(branch) {
		fmt.Println("Naming the worktree via Claude…")
		branch = claude.EnrichBranchName(context.Background(), repoRoot, hint, branch)
	}
	return branch
}

func directCreate(cfg config.Config, repoRoot, kind, hint, baseOverride string) {
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
	promptText, err := prompt.Render(cfg, kind, hint, base)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not render prompt: %v\n", err)
	}
	if err := os.WriteFile(wtPath+"/.worktree.md", []byte(promptText), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not write prompt: %v\n", err)
	}

	upsertSession(repoRoot, kind, branch, wtPath, hint)
	writeCdTarget(wtPath)

	clearScreen()
	tui.LaunchClaude(wtPath, false)
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


// cmdActivityTick bumps last_activity_at for the session matching the current
// repo + branch. Called by the activity-log.py hook on every Claude tool use.
// Silent on no-op; never fails the caller.
// extractFromFlag pulls `--from <branch>` or `-b <branch>` out of args. Returns
// the chosen base (or "" if absent) and the remaining args in original order.
func extractFromFlag(args []string) (string, []string) {
	out := make([]string, 0, len(args))
	base := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case (a == "--from" || a == "-b") && i+1 < len(args):
			base = args[i+1]
			i++ // skip the value
		case strings.HasPrefix(a, "--from="):
			base = strings.TrimPrefix(a, "--from=")
		default:
			out = append(out, a)
		}
	}
	return base, out
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
		Number      int    `json:"number"`
		Title       string `json:"title"`
		Author      struct{ Login string } `json:"author"`
		BaseRefName string `json:"baseRefName"`
		HeadRefName string `json:"headRefName"`
		IsDraft     bool   `json:"isDraft"`
		UpdatedAt   time.Time `json:"updatedAt"`
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
		diffOut  []byte
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
			Path:      c.Path,
			Line:      ln,
			Body:      c.Body,
			Outdated:  c.Line == 0,
			IsReply:   c.InReplyTo != 0,
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
		Number      int    `json:"number"`
		Title       string `json:"title"`
		Author      struct{ Login string } `json:"author"`
		BaseRefName string `json:"baseRefName"`
		HeadRefName string `json:"headRefName"`
		Commits     []struct {
			OID        string `json:"oid"`
			MessageHeadline string `json:"messageHeadline"`
			Authors    []struct {
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

	// Bare `work diff` → current uncommitted changes (staged + unstaged vs HEAD).
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
	// `@{u}`, etc.). Otherwise fall back to the PR target from config.
	var ref, target string
	if baseOverride != "" {
		ref = baseOverride
		// For display purposes, strip a leading `origin/` so the header shows
		// just the branch name.
		target = strings.TrimPrefix(baseOverride, "origin/")
	} else {
		target = resolvePRTarget(cfg, branch)
		if target == "" {
			fmt.Fprintln(os.Stderr, "error: no pr_base or base_branches configured (use --base <ref> to override)")
			os.Exit(1)
		}
		ref = "origin/" + target
	}
	if branch == target {
		fmt.Printf("On %s — nothing to compare.\n", target)
		return
	}
	// In dual-base workflows (branch from master, PR to user_test) the branch
	// will never be a descendant of the PR target — that's expected, not a
	// pollution warning. Skip the ancestor check.
	warn := ""

	commitCountRaw, _ := gitOutput(repoRoot, "git", "rev-list", "--count", ref+"..HEAD")
	commitCount, _ := strconv.Atoi(strings.TrimSpace(commitCountRaw))

	numstat, _ := gitOutput(repoRoot, "git", "diff", "--numstat", ref+"...HEAD")
	files := parseNumstat(numstat)

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
// staging (user_test).
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
	fmt.Fprintf(&b, "# %s — work project config\n", repo)
	fmt.Fprintf(&b, "# stack detected: %s · default branch: %s\n", stack, base)
	b.WriteString("# Edit to taste, then `work --doctor` to validate.\n\n")

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

	b.WriteString("# clickup:\n#   lists:\n#     features:   \"\"\n#     operations: \"\"\n\n")

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

	b.WriteString("# Named pointers at canonical docs. Pull with `work --refresh-docs`.\n")
	b.WriteString("# Example:\n#   pr_guidelines: ~/Documents/GitHub/team-docs/pr-guidelines.md\ndocs: {}\n")

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
	branch := strings.TrimSpace(currentBranch(repoRoot))
	if branch == "" {
		return
	}
	repo := originRepoName(repoRoot)
	id := state.MakeID(repo, branch)

	store, err := state.Load()
	if err != nil {
		return
	}
	if !store.Tick(id) {
		// Session unknown — record one so the dashboard surfaces it.
		// We don't know kind here; best guess from branch prefix.
		kind := "task"
		if strings.HasPrefix(branch, "review/") {
			kind = "review"
		}
		store.Upsert(state.Session{
			ID:             id,
			Repo:           repo,
			Branch:         branch,
			Kind:           kind,
			Path:           repoRoot,
			Status:         state.StatusActive,
			StartedAt:      time.Now(),
			LastActivityAt: time.Now(),
		})
	}
	_ = store.Save()
}

func cmdDashboard(cfg config.Config, repoRoot string) {
	scope := ""
	if repoRoot != "" {
		scope = originRepoName(repoRoot)
	}
	dash := tui.NewDashboard(cfg, scope)
	p := tea.NewProgram(dash, tea.WithAltScreen())
	m, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "dashboard error: %v\n", err)
		os.Exit(1)
	}
	result := m.(tui.Dashboard).Result()
	switch result.Action {
	case tui.ResultCd:
		// Parent-shell cd via the work() wrapper — no nested subshell.
		writeCdTarget(result.Path)
		clearScreen()
	case tui.ResultResume:
		if result.Path != "" {
			clearScreen()
			tui.LaunchClaude(result.Path, true)
		}
	}
}

func currentBranch(repoRoot string) string {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// upsertSession records a newly created worktree to the session store so the
// dashboard sees it immediately, before any activity-tick fires.
func upsertSession(repoRoot, kind, branch, wtPath, hint string) {
	repo := originRepoName(repoRoot)
	id := state.MakeID(repo, branch)
	store, err := state.Load()
	if err != nil {
		return
	}
	clickup := extractClickUp(hint)
	store.Upsert(state.Session{
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

// extractClickUp pulls the first CU-<id> or bare numeric id from a hint string.
func extractClickUp(s string) string {
	// Look for CU-<alnum>
	for _, prefix := range []string{"CU-", "cu-"} {
		if i := strings.Index(s, prefix); i >= 0 {
			rest := s[i+len(prefix):]
			end := 0
			for end < len(rest) && (rest[end] == '_' || rest[end] == '-' ||
				(rest[end] >= 'a' && rest[end] <= 'z') ||
				(rest[end] >= 'A' && rest[end] <= 'Z') ||
				(rest[end] >= '0' && rest[end] <= '9')) {
				end++
			}
			return rest[:end]
		}
	}
	return ""
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
  norn                    Cross-session dashboard (default view)
  norn "hint"             Create task worktree with hint (base: pr_base default)
  norn "hint" --from <b>  Override base branch for this worktree
  norn --review "hint"    Create review worktree
  norn -d, --dir          Main menu in cd-mode: enter cd's into the worktree,
                          l launches Claude
  norn --cd               Jump into a worktree shell (picker)
  norn --clean            Jump to clean view
  norn --list             List worktrees (git)
  norn --status           Show worktrees with details
  norn --project-config   Print resolved config as JSON
  norn --dashboard        Same as bare norn: live TUI of all known sessions
  norn diff               TUI diff of current uncommitted changes (working tree)
  norn diff --base [ref]  Compare committed branch vs a base: no ref = pr_base,
                          or any local/remote ref (origin/HEAD, master, @{u}, …)
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
  norn --help             This help`)
}

func clearScreen() {
	fmt.Print("\033[H\033[2J")
}
