package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sandbye/norn/internal/config"
	"github.com/sandbye/norn/internal/git"
	"github.com/sandbye/norn/internal/prompt"
	"github.com/sandbye/norn/internal/task"
)

// briefOutput is the JSON contract of `norn brief`. Stable field names: external
// tools (skuld) read this instead of reimplementing branch naming and config
// resolution. Slices are always present (never null) so consumers don't have to
// special-case an absent key.
type briefOutput struct {
	RepoRoot string      `json:"repo_root"`
	RepoName string      `json:"repo_name"`
	Branch   string      `json:"branch"`
	Base     string      `json:"base"`
	Kind     string      `json:"kind"`
	Hint     string      `json:"hint"`
	Template string      `json:"template"`
	Brief    string      `json:"brief"`
	Issue    *briefIssue `json:"issue,omitempty"`
	Config   briefConfig `json:"config"`
}

type briefIssue struct {
	Number int      `json:"number"`
	Title  string   `json:"title"`
	URL    string   `json:"url"`
	Kind   string   `json:"kind,omitempty"` // branch prefix implied by labels, advisory
	Labels []string `json:"labels"`
}

// briefConfig is the resolved project policy a caller needs to run the work:
// what to verify, what not to write, what to branch from. Sources lists the
// config files that actually existed, so an empty verify list is distinguishable
// from a repo with no project config at all.
type briefConfig struct {
	Sources      []string            `json:"sources"`
	Verify       []string            `json:"verify"`
	DoneWhen     []string            `json:"done_when"`
	Setup        string              `json:"setup"`
	BaseBranches []string            `json:"base_branches"`
	BranchBase   string              `json:"branch_base"`
	PRBase       string              `json:"pr_base"`
	Forbid       []config.ForbidRule `json:"forbid"`
	Format       []config.FormatRule `json:"format"`
	Review       []string            `json:"review"`
}

type briefFlags struct {
	repo     string
	issue    int
	hint     string
	kind     string
	typ      string
	base     string
	template string
}

// parseBriefFlags parses the flags of `norn brief`. Kept separate from the
// command so the flag contract is testable without a repo.
func parseBriefFlags(args []string) (briefFlags, error) {
	f := briefFlags{kind: "task"}
	next := func(i int, name string) (string, int, error) {
		if i+1 >= len(args) {
			return "", i, fmt.Errorf("%s needs a value", name)
		}
		return args[i+1], i + 1, nil
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		var v string
		var err error
		switch {
		case a == "--repo" || a == "-r":
			v, i, err = next(i, a)
			f.repo = v
		case strings.HasPrefix(a, "--repo="):
			f.repo = strings.TrimPrefix(a, "--repo=")
		case a == "--issue" || a == "-i":
			v, i, err = next(i, a)
			f.issue, err = briefIssueNumber(v, err)
		case strings.HasPrefix(a, "--issue="):
			f.issue, err = briefIssueNumber(strings.TrimPrefix(a, "--issue="), nil)
		case a == "--hint":
			v, i, err = next(i, a)
			f.hint = v
		case strings.HasPrefix(a, "--hint="):
			f.hint = strings.TrimPrefix(a, "--hint=")
		case a == "--kind":
			v, i, err = next(i, a)
			f.kind = v
		case strings.HasPrefix(a, "--kind="):
			f.kind = strings.TrimPrefix(a, "--kind=")
		case a == "--type":
			v, i, err = next(i, a)
			f.typ = v
		case strings.HasPrefix(a, "--type="):
			f.typ = strings.TrimPrefix(a, "--type=")
		case a == "--base" || a == "-b":
			v, i, err = next(i, a)
			f.base = v
		case strings.HasPrefix(a, "--base="):
			f.base = strings.TrimPrefix(a, "--base=")
		case a == "--template" || a == "-t":
			v, i, err = next(i, a)
			f.template = v
		case strings.HasPrefix(a, "--template="):
			f.template = strings.TrimPrefix(a, "--template=")
		default:
			return f, fmt.Errorf("unknown flag %q", a)
		}
		if err != nil {
			return f, err
		}
	}
	if f.issue == 0 && strings.TrimSpace(f.hint) == "" {
		return f, fmt.Errorf("need --issue <n> or --hint <text>")
	}
	if f.typ != "" && !isBranchType(f.typ) {
		return f, fmt.Errorf("--type must be one of feature|fix|hotfix|epic|chore, got %q", f.typ)
	}
	return f, nil
}

func briefIssueNumber(v string, err error) (int, error) {
	if err != nil {
		return 0, err
	}
	n, convErr := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(v), "#"))
	if convErr != nil || n <= 0 {
		return 0, fmt.Errorf("--issue must be a positive number, got %q", v)
	}
	return n, nil
}

func isBranchType(t string) bool {
	switch t {
	case "feature", "fix", "hotfix", "epic", "chore":
		return true
	}
	return false
}

// cmdBrief is `norn brief` — everything norn knows about a task, as JSON, with
// nothing created. No worktree, no branch, no push, no agent, no AI naming: the
// caller (a bot holding only a bare mirror) does the acting. cwdRepo is the repo
// norn resolved from the cwd, used when --repo is omitted.
func cmdBrief(cwdRepo string, args []string) {
	f, err := parseBriefFlags(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "norn brief: %v\nusage: norn brief --repo <path> --issue <n> [--hint <text>] [--type <prefix>] [--base <branch>] [--template <name>]\n", err)
		os.Exit(1)
	}

	repoRoot, err := briefRepoRoot(f.repo, cwdRepo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "norn brief: %v\n", err)
		os.Exit(1)
	}

	// Re-resolve config against the target repo: main() loaded it from the cwd,
	// which for a headless caller is unrelated to --repo.
	cfg, err := config.Load(repoRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "norn brief: config error: %v\n", err)
		os.Exit(1)
	}
	applyTemplateDir(cfg)

	kind := f.kind
	if kind == "" {
		kind = "task"
	}

	var (
		taskRef *prompt.TaskRef
		issue   *briefIssue
	)
	if f.issue > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		t, err := task.GitHubIssue(ctx, repoRoot, f.issue)
		if err != nil {
			fmt.Fprintf(os.Stderr, "norn brief: %v\n", err)
			os.Exit(1)
		}
		taskRef = &prompt.TaskRef{ID: t.ID, Title: t.Title, URL: t.URL, Description: t.Description}
		issue = &briefIssue{Number: f.issue, Title: t.Title, URL: t.URL, Kind: t.Kind, Labels: orEmpty(t.Labels)}
	}

	hint := f.hint
	if hint == "" && taskRef != nil {
		// Same shape the New tab writes for a picked issue, so a brief and a
		// `norn create` from the same issue name the branch identically.
		hint = fmt.Sprintf("#%s %s", taskRef.ID, taskRef.Title)
	}

	base := resolveBranchBase(cfg, repoRoot, f.base)

	// An explicit --type forces the branch prefix; MakeBranch reads the type from
	// the hint text, so prepend it there rather than duplicating its prefix rules.
	branchHint := hint
	if f.typ != "" {
		branchHint = f.typ + " " + hint
	}

	tmpl := prompt.Resolve(cfg, kind, f.template)
	if f.template != "" && !prompt.Has(f.template) {
		fmt.Fprintf(os.Stderr, "norn brief: template %q not found, using %q\n", f.template, tmpl)
	}
	briefText, err := prompt.Render(cfg, kind, hint, base, tmpl, taskRef)
	if err != nil {
		fmt.Fprintf(os.Stderr, "norn brief: %v\n", err)
		os.Exit(1)
	}

	out := briefOutput{
		RepoRoot: repoRoot,
		RepoName: config.RepoName(repoRoot),
		Branch:   git.MakeBranch(kind, branchHint),
		Base:     base,
		Kind:     kind,
		Hint:     hint,
		Template: tmpl,
		Brief:    briefText,
		Issue:    issue,
		Config: briefConfig{
			Sources:      orEmpty(config.Sources(repoRoot)),
			Verify:       orEmpty(cfg.Verify),
			DoneWhen:     orEmpty(cfg.DoneWhen),
			Setup:        cfg.Setup,
			BaseBranches: orEmpty(cfg.BaseBranches),
			BranchBase:   cfg.BranchBase,
			PRBase:       cfg.PRBase,
			Forbid:       orEmpty(cfg.Forbid),
			Format:       orEmpty(cfg.Format),
			Review:       orEmpty(cfg.Review),
		},
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false) // forbid patterns read as written
	if err := enc.Encode(out); err != nil {
		fmt.Fprintf(os.Stderr, "norn brief: %v\n", err)
		os.Exit(1)
	}
}

// briefRepoRoot resolves the repo to describe: --repo if given (a checkout, a
// worktree, or a bare mirror), else the repo norn found from the cwd.
func briefRepoRoot(repoFlag, cwdRepo string) (string, error) {
	if repoFlag == "" {
		if cwdRepo == "" {
			return "", fmt.Errorf("not inside a git repository — pass --repo <path>")
		}
		return cwdRepo, nil
	}
	dir, err := filepath.Abs(expandHome(repoFlag))
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(dir); err != nil {
		return "", fmt.Errorf("--repo %s: %w", repoFlag, err)
	}
	return git.RepoRootAt(dir)
}

// orEmpty keeps JSON arrays as [] rather than null for absent config.
func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
