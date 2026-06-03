package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sandbye/work/internal/config"
	"github.com/sandbye/work/internal/git"
	"github.com/sandbye/work/internal/prompt"
	"github.com/sandbye/work/internal/state"
	"github.com/sandbye/work/internal/tui"
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
		case "--dashboard", "-d":
			cmdDashboard(cfg, repoRoot)
			return
		case "--refresh-docs":
			cmdRefreshDocs()
			return
		case "--diff", "diff":
			plain := false
			prNum := ""
			for _, a := range args[1:] {
				switch {
				case a == "--plain" || a == "-p":
					plain = true
				case strings.HasPrefix(a, "#"):
					prNum = strings.TrimPrefix(a, "#")
				case isAllDigits(a):
					prNum = a
				}
			}
			if prNum != "" {
				cmdDiffPR(cfg, repoRoot, prNum, plain)
				return
			}
			cmdDiff(cfg, repoRoot, plain)
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
	switch {
	case len(args) > 0 && args[0] == "--clean":
		initialView = tui.ViewClean
	case len(args) > 0 && args[0] == "--cd":
		initialView = tui.ViewCd
	}

	// Direct create: `work "some hint"` or `work --review "hint"`
	if len(args) > 0 && args[0] != "--clean" && args[0] != "--cd" {
		if repoRoot == "" {
			fmt.Fprintln(os.Stderr, "error: not inside a git repository")
			os.Exit(1)
		}
		kind := "task"
		hint := strings.Join(args, " ")
		if args[0] == "--review" {
			kind = "review"
			hint = strings.Join(args[1:], " ")
		}
		directCreate(cfg, repoRoot, kind, hint)
		return
	}

	// Outside any git repo with no other intent → open the cross-session
	// dashboard. Lets you `work` from any random terminal pane.
	if repoRoot == "" && initialView == tui.ViewMenu {
		cmdDashboard(cfg, "")
		return
	}
	if repoRoot == "" && initialView != tui.ViewClean {
		fmt.Fprintln(os.Stderr, "error: not inside a git repository")
		os.Exit(1)
	}

	reapStale(cfg, repoRoot)

	app := tui.NewApp(cfg, repoRoot, initialView)
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
		clearScreen()
		tui.LaunchClaude(result.Path, false)
	case tui.ResultResume:
		upsertSessionFromPath(repoRoot, cfg.WorktreeDir, result.Path)
		clearScreen()
		tui.LaunchClaude(result.Path, true)
	case tui.ResultCd:
		clearScreen()
		launchShell(result.Path)
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
	return string(data)
}

// reapStale prompts to remove worktrees whose remote branch is gone (PR merged
// or branch deleted). Prevents GitHub Desktop / other clients from looping on
// "can't delete branch — worktree using it".
func reapStale(cfg config.Config, repoRoot string) {
	if repoRoot == "" {
		return
	}
	// Parallel: fetch + prune remote refs while we list local worktrees.
	var wts []git.Worktree
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = git.FetchPrune(repoRoot)
	}()
	go func() {
		defer wg.Done()
		common, _ := git.CommonDir(repoRoot)
		wts, _ = git.ListWorktrees(cfg.WorktreeDir, common)
	}()
	wg.Wait()

	if len(wts) == 0 {
		_ = git.PruneWorktrees(repoRoot)
		return
	}
	wts = git.CheckRemoteGone(repoRoot, wts)

	var stale []git.Worktree
	for _, wt := range wts {
		if wt.RemoteGone {
			stale = append(stale, wt)
		}
	}
	if len(stale) == 0 {
		_ = git.PruneWorktrees(repoRoot)
		return
	}

	fmt.Printf("\n%d worktree(s) with deleted remote branches:\n", len(stale))
	for _, wt := range stale {
		fmt.Printf("  %s  (%s)\n", wt.Branch, git.Age(wt.LastCommit))
	}
	fmt.Print("Clean these? [y/N] ")

	reader := bufio.NewReader(os.Stdin)
	ans, _ := reader.ReadString('\n')
	if strings.ToLower(strings.TrimSpace(ans)) != "y" {
		return
	}

	for _, wt := range stale {
		if err := git.RemoveWorktree(repoRoot, wt.Path, wt.Branch); err != nil {
			fmt.Fprintf(os.Stderr, "  failed %s: %v\n", wt.Branch, err)
		} else {
			fmt.Printf("  removed %s\n", wt.Branch)
		}
	}
	git.CleanEmptyDirs(cfg.WorktreeDir)
}

func directCreate(cfg config.Config, repoRoot, kind, hint string) {
	// Default base: pr_base (single source of truth for branch + PR target).
	// Falls back to first base_branches entry, then "master".
	base := "master"
	switch {
	case cfg.PRBase != "":
		base = cfg.PRBase
	case len(cfg.BaseBranches) > 0:
		base = cfg.BaseBranches[0]
	}

	branch := git.MakeBranch(kind, hint)
	fmt.Printf("Creating worktree: %s (base: %s)\n", branch, base)

	wtPath, err := git.CreateWorktree(repoRoot, cfg.WorktreeDir, branch, base)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	_ = git.SymlinkEnvFiles(repoRoot, wtPath)

	// Generate prompt
	promptText, err := prompt.Render(cfg, kind, hint)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not render prompt: %v\n", err)
	}
	if err := os.WriteFile(wtPath+"/.worktree.md", []byte(promptText), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not write prompt: %v\n", err)
	}

	upsertSession(repoRoot, kind, branch, wtPath, hint)

	clearScreen()
	tui.LaunchClaude(wtPath, false)
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

// cmdDiffPR shows the diff for any open PR (yours or a colleague's). Uses
// `gh pr diff` so no local checkout is needed. Renders in the same TUI as
// the local-branch view, with PR metadata in the header instead of "vs branch".
func cmdDiffPR(cfg config.Config, repoRoot, prNum string, plain bool) {
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

	// Fetch the full diff once. Parse per-file from there.
	diffOut, err := exec.Command("gh", "pr", "diff", prNum).Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: gh pr diff failed: %v\n", err)
		os.Exit(1)
	}
	files, perFileDiff := splitDiffByFile(string(diffOut))

	if plain {
		printDiffPlain(meta.BaseRef, meta.Commits, files, "")
		return
	}

	dv := tui.NewPRDiffView(repoRoot, meta, files, perFileDiff)
	p := tea.NewProgram(dv, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "diff TUI error: %v\n", err)
		os.Exit(1)
	}
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
		Commits     []any  `json:"commits"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return tui.PRMeta{}, fmt.Errorf("parse pr view: %w", err)
	}
	return tui.PRMeta{
		Number:  raw.Number,
		Title:   raw.Title,
		Author:  raw.Author.Login,
		BaseRef: raw.BaseRefName,
		HeadRef: raw.HeadRefName,
		Commits: len(raw.Commits),
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
func cmdDiff(cfg config.Config, repoRoot string, plain bool) {
	if repoRoot == "" {
		fmt.Fprintln(os.Stderr, "error: not inside a git repository")
		os.Exit(1)
	}
	target := resolvePRBase(cfg)
	if target == "" {
		fmt.Fprintln(os.Stderr, "error: no pr_base or base_branches configured")
		os.Exit(1)
	}

	branch := strings.TrimSpace(currentBranch(repoRoot))
	if branch == "" {
		fmt.Fprintln(os.Stderr, "error: could not determine current branch")
		os.Exit(1)
	}
	if branch == target {
		fmt.Printf("On %s — nothing to compare.\n", target)
		return
	}

	ref := "origin/" + target
	ancestorErr := exec.Command("git", "-C", repoRoot, "merge-base", "--is-ancestor", ref, "HEAD").Run()
	mergeBase, _ := gitOutput(repoRoot, "git", "merge-base", ref, "HEAD")
	mergeBase = strings.TrimSpace(mergeBase)

	warn := ""
	if ancestorErr != nil {
		warn = fmt.Sprintf(
			"⚠ branch not forked from %s · actual fork: %s · diff includes commits not in %s · fix: `git rebase --onto %s <actual-base> HEAD`",
			target, shortSha(mergeBase), ref, ref,
		)
	}

	commitCountRaw, _ := gitOutput(repoRoot, "git", "rev-list", "--count", ref+"..HEAD")
	commitCount, _ := strconv.Atoi(strings.TrimSpace(commitCountRaw))

	numstat, _ := gitOutput(repoRoot, "git", "diff", "--numstat", ref+"...HEAD")
	files := parseNumstat(numstat)

	if plain {
		printDiffPlain(target, commitCount, files, warn)
		return
	}

	dv := tui.NewDiffView(repoRoot, ref, commitCount, files, warn)
	p := tea.NewProgram(dv, tea.WithAltScreen())
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

func gitOutput(dir string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

func shortSha(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
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

// groupByDir parses git diff --numstat output and groups by top two path
// components, falling back to top-1 if shallow.
func groupByDir(numstat string) []diffGroup {
	byDir := map[string]*diffGroup{}
	var order []string

	for _, line := range strings.Split(numstat, "\n") {
		parts := strings.Fields(line)
		if len(parts) < 3 {
			continue
		}
		added, _ := strconv.Atoi(parts[0])
		removed, _ := strconv.Atoi(parts[1])
		path := parts[2]
		// "-" means binary file
		dir := topTwoDirs(path)
		if _, ok := byDir[dir]; !ok {
			byDir[dir] = &diffGroup{dir: dir}
			order = append(order, dir)
		}
		g := byDir[dir]
		g.files = append(g.files, diffFile{relPath: relTo(path, dir), added: added, removed: removed})
		g.added += added
		g.removed += removed
	}

	out := make([]diffGroup, 0, len(order))
	for _, d := range order {
		g := byDir[d]
		// Sort files largest first within group
		sortFilesByChurn(g.files)
		out = append(out, *g)
	}
	return out
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
		checkBinary("git"),
		checkBinary("gh"),
		checkBinary("python3"),
		checkClaudeDir(home),
		checkHooksExecutable(home),
		checkSkillsPresent(home),
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
	fmt.Println("work --doctor")
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

func checkBinary(name string) doctorCheck {
	_, err := exec.LookPath(name)
	if err != nil {
		return doctorCheck{name: name + " in PATH", fail: true, fix: "install " + name}
	}
	return doctorCheck{name: name + " in PATH"}
}

func checkClaudeDir(home string) doctorCheck {
	p := filepath.Join(home, ".claude")
	if _, err := os.Stat(p); err != nil {
		return doctorCheck{name: "~/.claude exists", fail: true, fix: "install Claude Code"}
	}
	return doctorCheck{name: "~/.claude exists"}
}

func checkHooksExecutable(home string) doctorCheck {
	dir := filepath.Join(home, ".claude", "hooks")
	expected := []string{
		"check-stale.py", "track-read.py", "policy-patterns.py",
		"policy-push.py", "policy-commit.py", "format-on-write.py", "activity-log.py",
	}
	var missing, notExec []string
	for _, f := range expected {
		p := filepath.Join(dir, f)
		info, err := os.Stat(p)
		if err != nil {
			missing = append(missing, f)
			continue
		}
		if info.Mode()&0o111 == 0 {
			notExec = append(notExec, f)
		}
	}
	switch {
	case len(missing) > 0:
		return doctorCheck{
			name:   "hooks present + executable",
			detail: "missing: " + strings.Join(missing, ", "),
			fix:    "ensure files exist in ~/.claude/hooks/",
			fail:   true,
		}
	case len(notExec) > 0:
		return doctorCheck{
			name:   "hooks present + executable",
			detail: "not executable: " + strings.Join(notExec, ", "),
			fix:    "chmod +x ~/.claude/hooks/" + strings.Join(notExec, " ~/.claude/hooks/"),
			fail:   true,
		}
	}
	return doctorCheck{name: "hooks present + executable"}
}

func checkSkillsPresent(home string) doctorCheck {
	dir := filepath.Join(home, ".claude", "skills")
	expected := []string{"start-task", "precheck", "open-pr", "pr-judge", "find-task"}
	var missing []string
	for _, s := range expected {
		if _, err := os.Stat(filepath.Join(dir, s, "SKILL.md")); err != nil {
			missing = append(missing, s)
		}
	}
	if len(missing) > 0 {
		return doctorCheck{
			name:   "personal skills present",
			detail: "missing: " + strings.Join(missing, ", "),
			fix:    "recreate from plan or reinstall",
			fail:   true,
		}
	}
	return doctorCheck{name: "personal skills present"}
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
		return doctorCheck{name: "project configs parse", detail: "no projects dir yet", warn: true, fix: "`work init` inside any repo"}
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
		return doctorCheck{name: "project configs parse", detail: "no project configs yet", warn: true, fix: "`work init` inside any repo"}
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
			fix:  "`work init` to scaffold a project config",
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
	if result.Action == tui.ResultResume && result.Path != "" {
		clearScreen()
		tui.LaunchClaude(result.Path, true)
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

func filepathBase(p string) string {
	return filepath.Base(p)
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
	fmt.Println(`work — worktree manager

Usage:
  work                    Interactive TUI
  work "hint"             Create task worktree with hint
  work --review "hint"    Create review worktree
  work --cd               Jump into a worktree shell
  work --clean            Jump to clean view
  work --list             List worktrees (git)
  work --status           Show worktrees with details
  work --project-config   Print resolved config as JSON
  work -d, --dashboard    Live TUI of all known sessions
                          (also opens by default when run outside any git repo)
  work diff               TUI diff vs pr_base (warn if forked from wrong base)
  work diff <pr#>         TUI diff of any open PR (yours or colleague's)
  work diff --plain       Plain text diff for piping
  work init               Scaffold a project config for the current repo
  work doctor             Diagnose hooks, skills, configs, docs, state
  work --refresh-docs     git pull every doc repo referenced in any project config
  work --activity-tick    Bump current session's last_activity_at (called by hook)
  work --help             This help`)
}

func clearScreen() {
	fmt.Print("\033[H\033[2J")
}

func launchShell(dir string) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	cmd := exec.Command(shell, "-i")
	cmd.Dir = dir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Run()
}
