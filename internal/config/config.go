package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// originRepoName returns the basename of the upstream/main repo for a given
// directory. Inside a worktree, this returns the original repo's name rather
// than the worktree dir name, so project config lookups stay stable.
func originRepoName(dir string) string {
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
	// common typically looks like ".../<repo>/.git". Repo dir is the parent.
	return filepath.Base(filepath.Dir(common))
}

type Config struct {
	WorktreeDir  string       `yaml:"worktree_dir" json:"worktree_dir"`
	ClickUp      *ClickUp     `yaml:"clickup,omitempty" json:"clickup,omitempty"`
	Verify       []string     `yaml:"verify,omitempty" json:"verify,omitempty"`
	Setup        string       `yaml:"setup,omitempty" json:"setup,omitempty"`
	BaseBranches []string     `yaml:"base_branches" json:"base_branches"`

	// PRBase is the default PR target — where finished branches merge back to.
	// e.g. user_test for staging-first workflows. Override per-PR with the
	// HotfixTarget rule below.
	PRBase string `yaml:"pr_base,omitempty" json:"pr_base,omitempty"`

	// BranchBase is the source branch new worktrees fork from. Often the
	// production branch (master) even when PRs target a staging branch. If
	// unset, falls back to PRBase, then git's detected default.
	BranchBase string `yaml:"branch_base,omitempty" json:"branch_base,omitempty"`

	// HotfixTarget is the PR target for branches whose name starts with
	// HotfixPrefix (default "hotfix/"). When set, /open-pr and `work diff`
	// route hotfix branches to this branch instead of PRBase.
	HotfixTarget string `yaml:"hotfix_target,omitempty" json:"hotfix_target,omitempty"`

	// HotfixPrefix is the branch-name prefix that triggers the hotfix routing.
	// Defaults to "hotfix/" if unset.
	HotfixPrefix string `yaml:"hotfix_prefix,omitempty" json:"hotfix_prefix,omitempty"`
	User         User         `yaml:"user" json:"user"`
	Templates    TemplatesDir `yaml:"templates,omitempty" json:"templates,omitempty"`

	// Forbid: patch-time policy rules. Hooks (policy-patterns.py) reject
	// Edit/Write payloads matching `Match`. Glob is fnmatch against repo-relative
	// path. Severity is "block" (default) or "warn".
	Forbid []ForbidRule `yaml:"forbid,omitempty" json:"forbid,omitempty"`

	// Format: post-write formatters. Hooks (format-on-write.py) run the matching
	// command with the file path appended.
	Format []FormatRule `yaml:"format,omitempty" json:"format,omitempty"`

	// Review: judge files for /pr-judge. Paths relative to repo root.
	Review []string `yaml:"review,omitempty" json:"review,omitempty"`

	// Docs: named pointers at canonical team / personal documentation. Skills
	// read these by key (e.g. `docs.pr_guidelines`) instead of baking rules in.
	// Paths can be absolute, ~-prefixed, or repo-relative.
	// `work --refresh-docs` pulls every git repo containing one of these paths.
	Docs map[string]string `yaml:"docs,omitempty" json:"docs,omitempty"`

	// DoneWhen: shell commands that constitute "actually done" beyond /precheck.
	// Read by the /done skill. Each entry is one criterion (functional check,
	// CI gate, translation sync, etc). Same execution model as `verify:`.
	DoneWhen []string `yaml:"done_when,omitempty" json:"done_when,omitempty"`
}

type User struct {
	Name       string `yaml:"name" json:"name"`
	Email      string `yaml:"email" json:"email"`
	ClickUpUID string `yaml:"clickup_uid,omitempty" json:"clickup_uid,omitempty"`
}

type ClickUp struct {
	Lists map[string]string `yaml:"lists,omitempty" json:"lists,omitempty"`
}

type TemplatesDir struct {
	Dir string `yaml:"dir,omitempty" json:"dir,omitempty"`
}

type ForbidRule struct {
	Match    string `yaml:"match" json:"match"`
	Reason   string `yaml:"reason,omitempty" json:"reason,omitempty"`
	Glob     string `yaml:"glob,omitempty" json:"glob,omitempty"`
	Severity string `yaml:"severity,omitempty" json:"severity,omitempty"` // "block" | "warn"
}

type FormatRule struct {
	Ext []string `yaml:"ext" json:"ext"`
	Cmd string   `yaml:"cmd" json:"cmd"`
}

func DefaultConfig() Config {
	home, _ := os.UserHomeDir()
	return Config{
		WorktreeDir:  filepath.Join(home, "worktrees"),
		BaseBranches: []string{"master", "main"},
		User: User{
			Name: "unknown",
		},
	}
}

// Load reads ~/.config/work/config.yaml merged with .work.yaml from repo root.
func Load(repoRoot string) (Config, error) {
	cfg := DefaultConfig()

	home, err := os.UserHomeDir()
	if err != nil {
		return cfg, err
	}

	// Global config
	globalPath := filepath.Join(home, ".config", "work", "config.yaml")
	if err := mergeFromFile(&cfg, globalPath); err != nil && !os.IsNotExist(err) {
		return cfg, err
	}

	// Project config: repo root first, then ~/.config/work/projects/<repo-name>.yaml
	if repoRoot != "" {
		projectPath := filepath.Join(repoRoot, ".work.yaml")
		if err := mergeFromFile(&cfg, projectPath); err != nil && !os.IsNotExist(err) {
			return cfg, err
		}

		// Use the *origin* repo name (not the worktree basename) so the same
		// config matches whether we're in the main checkout or any worktree.
		repoName := originRepoName(repoRoot)
		localProjectPath := filepath.Join(home, ".config", "work", "projects", repoName+".yaml")
		if err := mergeFromFile(&cfg, localProjectPath); err != nil && !os.IsNotExist(err) {
			return cfg, err
		}
	}

	// Expand ~ in worktree_dir
	if strings.HasPrefix(cfg.WorktreeDir, "~/") {
		cfg.WorktreeDir = filepath.Join(home, cfg.WorktreeDir[2:])
	}

	return cfg, nil
}

func mergeFromFile(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(data, cfg)
}

// UnmarshalYAML exposes yaml decoding so other packages can parse Config
// without importing gopkg.in/yaml.v3 themselves.
func UnmarshalYAML(data []byte, v any) error {
	return yaml.Unmarshal(data, v)
}
