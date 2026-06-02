package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
			cmdDashboard(cfg)
			return
		case "--refresh-docs":
			cmdRefreshDocs()
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
		cmdDashboard(cfg)
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
	// Pick first base branch
	base := "master"
	if len(cfg.BaseBranches) > 0 {
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
// cmdInit scaffolds a project config for the current repo. Detects stack,
// suggests verify commands + base branches. Refuses to overwrite an existing
// file — tells you where it lives so you can open it yourself.
func cmdInit(repoRoot string) {
	if repoRoot == "" {
		fmt.Fprintln(os.Stderr, "error: not inside a git repository")
		os.Exit(1)
	}
	home, _ := os.UserHomeDir()
	repoName := filepath.Base(repoRoot)
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
	repoName := filepath.Base(repoRoot)
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
	repo := filepathBase(repoRoot)
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

func cmdDashboard(cfg config.Config) {
	dash := tui.NewDashboard(cfg)
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
	repo := filepathBase(repoRoot)
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
		out["repo_name"] = filepathBase(repoRoot)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}

func filepathBase(p string) string {
	return filepath.Base(p)
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
