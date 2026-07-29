package prompt

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/sandbye/norn/internal/config"
)

const tmplExt = ".md.tmpl"

//go:embed templates/*.md.tmpl
var embedded embed.FS

// overrideDir is where user template overrides live. Empty = the default
// (~/.config/work/templates). SetTemplateDir wires the `templates.dir` config.
var overrideDir string

// SetTemplateDir points the user-override lookup at dir (called once at startup
// from the resolved config). Empty keeps the default.
func SetTemplateDir(dir string) { overrideDir = dir }

// DataFields lists the fields a template can reference, for `norn --templates`.
func DataFields() []string {
	return []string{
		".User.Name / .User.Email / .User.ClickUpUID",
		".ClickUp.Lists (map name→id; nil-gate with {{if .ClickUp}})",
		".Verify (commands)  .Setup (setup command)",
		".Base (fork branch)  .PRBase (PR target)",
		".HintBlock  .Kind  .Generated",
		"funcs: plus1 · default · upper · lower · join",
	}
}

// TaskRef is a tracker task selected at create time, baked into the brief so
// the agent has it up front instead of re-fetching over MCP.
type TaskRef struct {
	ID          string
	Title       string
	URL         string
	Description string
}

// PRRef is the pull request a review worktree is checked out to, baked into the
// review brief so the agent knows exactly what it's reviewing and against what.
type PRRef struct {
	Number int
	Title  string
	URL    string
	Base   string // the PR's base branch — diff HEAD against this
}

// Data holds all values available to templates.
type Data struct {
	Kind      string // "task" or "review"
	Hint      string // the raw hint (frontmatter); HintBlock is the prose form
	HintBlock string
	User      config.User
	ClickUp   *config.ClickUp
	Verify    []string
	Setup     string
	Base      string // branch this worktree was forked from (diff baseline)
	PRBase    string // default PR target (cfg.pr_base — may equal Base or differ)
	Task      *TaskRef
	PR        *PRRef // set for review worktrees (norn review <pr#>)
	Generated string
}

// Render renders template `tmpl` with cfg + hint. When `tmpl` is empty it
// falls back to the template named after `kind`. `kind` (task/review) still
// drives the hint block and workflow data regardless of which file renders.
// `base` is the branch the worktree was forked from — agents must diff against
// it, not assume `master`.
func Render(cfg config.Config, kind, hint, base, tmpl string, taskRef *TaskRef) (string, error) {
	if tmpl == "" {
		tmpl = kind
	}
	tmplName := tmpl + ".md.tmpl"

	data := Data{
		Kind:      kind,
		Hint:      hint,
		HintBlock: hintBlock(kind, hint),
		User:      cfg.User,
		ClickUp:   cfg.ClickUp,
		Verify:    cfg.Verify,
		Setup:     cfg.Setup,
		Base:      base,
		PRBase:    cfg.PRBase,
		Task:      taskRef,
		Generated: time.Now().Format("2006-01-02 15:04"),
	}

	return renderNamed(tmplName, data)
}

// RenderReview renders the review brief for a PR-checkout worktree (norn review
// <pr#>). `tmpl` is the template name (empty → "review"); a user override of
// that name still wins over the built-in.
func RenderReview(cfg config.Config, tmpl string, pr *PRRef) (string, error) {
	if tmpl == "" {
		tmpl = "review"
	}
	data := Data{
		Kind:      "review",
		Hint:      pr.Title,
		HintBlock: hintBlock("review", pr.Title),
		User:      cfg.User,
		ClickUp:   cfg.ClickUp,
		Verify:    cfg.Verify,
		Setup:     cfg.Setup,
		Base:      pr.Base,
		PRBase:    cfg.PRBase,
		PR:        pr,
		Generated: time.Now().Format("2006-01-02 15:04"),
	}
	return renderNamed(tmpl+".md.tmpl", data)
}

// renderNamed loads a template by filename — user override dir first, then the
// embedded built-in — and renders it with data.
func renderNamed(tmplName string, data Data) (string, error) {
	if content, err := os.ReadFile(filepath.Join(userTemplateDir(), tmplName)); err == nil {
		return render(string(content), tmplName, data)
	}
	content, err := embedded.ReadFile("templates/" + tmplName)
	if err != nil {
		return "", fmt.Errorf("template %q not found: %w", tmplName, err)
	}
	return render(string(content), tmplName, data)
}

// userTemplateDir is where user template overrides live: the configured
// `templates.dir`, or ~/.config/work/templates by default.
func userTemplateDir() string {
	if overrideDir != "" {
		return overrideDir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "work", "templates")
}

// List returns the available template names (basename without the .md.tmpl
// suffix), the union of the built-in templates and any user overrides, sorted.
// A user override and a built-in of the same name appear once.
func List() []string {
	set := map[string]bool{}

	if entries, err := embedded.ReadDir("templates"); err == nil {
		for _, e := range entries {
			if name, ok := strings.CutSuffix(e.Name(), tmplExt); ok {
				set[name] = true
			}
		}
	}
	if entries, err := os.ReadDir(userTemplateDir()); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if name, ok := strings.CutSuffix(e.Name(), tmplExt); ok {
				set[name] = true
			}
		}
	}

	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Has reports whether a template of the given name resolves (user or built-in).
func Has(name string) bool {
	if name == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(userTemplateDir(), name+tmplExt)); err == nil {
		return true
	}
	_, err := embedded.ReadFile("templates/" + name + tmplExt)
	return err == nil
}

// NewTemplate scaffolds a user template <name> in the templates dir, seeded
// from the built-in task template. Returns the created path; errors if it
// already exists so we never clobber a user's work.
func NewTemplate(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("template name required")
	}
	dir := userTemplateDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name+tmplExt)
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("%s already exists", path)
	}
	starter, err := embedded.ReadFile("templates/task" + tmplExt)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, starter, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// EnsureUserTemplate returns the path to the user-editable copy of template
// `name` (default "task"), seeding it from the matching built-in — or the task
// template — if it doesn't exist yet. This is how a user customizes even the
// default templates: edit the copy, which then shadows the built-in.
func EnsureUserTemplate(name string) (string, error) {
	if name == "" {
		name = "task"
	}
	dir := userTemplateDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name+tmplExt)
	if _, err := os.Stat(path); err == nil {
		return path, nil // already a user copy
	}
	seed, err := embedded.ReadFile("templates/" + name + tmplExt)
	if err != nil {
		if seed, err = embedded.ReadFile("templates/task" + tmplExt); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(path, seed, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// Resolve picks the template name for a create: an explicit override wins;
// otherwise a task honors cfg.Template; review (and an empty default) fall back
// to the kind's own template. An override that doesn't resolve is ignored.
func Resolve(cfg config.Config, kind, override string) string {
	if override != "" && Has(override) {
		return override
	}
	if kind == "task" && cfg.Template != "" && Has(cfg.Template) {
		return cfg.Template
	}
	return kind
}

func render(tmplStr, name string, data Data) (string, error) {
	funcMap := template.FuncMap{
		"plus1": func(i int) int { return i + 1 },
		"default": func(fallback, v string) string {
			if v == "" {
				return fallback
			}
			return v
		},
		"upper": strings.ToUpper,
		"lower": strings.ToLower,
		"join":  strings.Join,
	}
	t, err := template.New(name).Funcs(funcMap).Parse(tmplStr)
	if err != nil {
		return "", fmt.Errorf("parse template %q: %w", name, err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute template %q: %w", name, err)
	}
	return buf.String(), nil
}

// frontmatterHint reads the `hint:` field from a leading YAML frontmatter block.
var frontmatterHintRegex = regexp.MustCompile(`(?m)^hint:\s*(.+)$`)

// frontmatterTitleRegex reads the human task title: `title:` if present (what a
// later start-task writeback sets), else `task:` (what the task template writes
// when a ClickUp task is known at create time).
var frontmatterTitleRegex = regexp.MustCompile(`(?m)^(?:title|task):\s*(.+)$`)

// hintMarkerRegex matches the legacy prose markers `Hint: "..."` /
// `Review hint: "..."` for .worktree.md files written before frontmatter.
var hintMarkerRegex = regexp.MustCompile(`(?:Review hint|Hint):\s*"([^"]*)"`)

// ExtractHint recovers the original hint from a rendered .worktree.md so the
// dashboard stores the hint, not the whole file. Reads the frontmatter `hint:`
// field, falling back to the legacy prose markers. Returns "" if none.
func ExtractHint(content string) string {
	// Frontmatter: a leading --- … --- block.
	if strings.HasPrefix(content, "---") {
		if end := strings.Index(content[3:], "\n---"); end >= 0 {
			fm := content[3 : 3+end]
			if m := frontmatterHintRegex.FindStringSubmatch(fm); m != nil {
				v := strings.TrimSpace(m[1])
				if unq, err := strconv.Unquote(v); err == nil {
					return unq
				}
				return strings.Trim(v, `"`)
			}
		}
	}
	if m := hintMarkerRegex.FindStringSubmatch(content); m != nil {
		return m[1]
	}
	return ""
}

// ExtractTitle recovers the human task title from a rendered .worktree.md so the
// dashboard can label a row by what the task actually is, not just the branch.
// Reads the frontmatter `title:`/`task:` field. Returns "" if none (bare-hint
// worktrees have no title until start-task resolves + writes one back).
func ExtractTitle(content string) string {
	if strings.HasPrefix(content, "---") {
		if end := strings.Index(content[3:], "\n---"); end >= 0 {
			fm := content[3 : 3+end]
			if m := frontmatterTitleRegex.FindStringSubmatch(fm); m != nil {
				v := strings.TrimSpace(m[1])
				if unq, err := strconv.Unquote(v); err == nil {
					return unq
				}
				return strings.Trim(v, `"`)
			}
		}
	}
	return ""
}

// nextRegex reads the `next:` line from a .state.md. Unlike .worktree.md,
// .state.md is bare `key: value` lines (no `--- … ---` frontmatter), per the
// state-file contract, so this matches a top-level `next:` anywhere in the body.
var nextRegex = regexp.MustCompile(`(?m)^next:[ \t]*(.+?)[ \t]*$`)

// ExtractNext pulls the single `next:` action out of a .state.md body so the
// dashboard can show it without opening the file. Returns "" when the field is
// absent or the file is malformed, so a missing state file degrades silently.
func ExtractNext(content string) string {
	if m := nextRegex.FindStringSubmatch(content); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func hintBlock(kind, hint string) string {
	if hint == "" {
		if kind == "review" {
			return "No hint provided. Ask the user which PR or task to review."
		}
		return "No hint provided. Ask the user what to work on."
	}
	if kind == "review" {
		return fmt.Sprintf("Review hint: %q", hint)
	}
	return fmt.Sprintf("Hint: %q", hint)
}
