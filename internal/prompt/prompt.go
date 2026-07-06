package prompt

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/sandbye/norn/internal/config"
)

const tmplExt = ".md.tmpl"

//go:embed templates/*.md.tmpl
var embedded embed.FS

// Data holds all values available to templates.
type Data struct {
	Kind      string // "task" or "review"
	HintBlock string
	User      config.User
	ClickUp   *config.ClickUp
	Verify    []string
	Setup     string
	Base      string // branch this worktree was forked from (diff baseline)
	PRBase    string // default PR target (cfg.pr_base — may equal Base or differ)
	Generated string
}

// Render renders template `tmpl` with cfg + hint. When `tmpl` is empty it
// falls back to the template named after `kind`. `kind` (task/review) still
// drives the hint block and workflow data regardless of which file renders.
// `base` is the branch the worktree was forked from — agents must diff against
// it, not assume `master`.
func Render(cfg config.Config, kind, hint, base, tmpl string) (string, error) {
	if tmpl == "" {
		tmpl = kind
	}
	tmplName := tmpl + ".md.tmpl"

	data := Data{
		Kind:      kind,
		HintBlock: hintBlock(kind, hint),
		User:      cfg.User,
		ClickUp:   cfg.ClickUp,
		Verify:    cfg.Verify,
		Setup:     cfg.Setup,
		Base:      base,
		PRBase:    cfg.PRBase,
		Generated: time.Now().Format("2006-01-02 15:04"),
	}

	// Try user override first: ~/.config/work/templates/<kind>.md.tmpl
	home, _ := os.UserHomeDir()
	userPath := filepath.Join(home, ".config", "work", "templates", tmplName)
	if content, err := os.ReadFile(userPath); err == nil {
		return render(string(content), tmplName, data)
	}

	// Fall back to embedded
	content, err := embedded.ReadFile("templates/" + tmplName)
	if err != nil {
		return "", fmt.Errorf("template %q not found: %w", tmplName, err)
	}
	return render(string(content), tmplName, data)
}

// userTemplateDir is where user template overrides live.
func userTemplateDir() string {
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

// hintMarkerRegex matches the two markers `hintBlock` emits:
//
//	Hint: "..."
//	Review hint: "..."
//
// Used by ExtractHint to recover the original short string from a rendered
// .worktree.md so session state stores the hint, not the whole prompt file.
var hintMarkerRegex = regexp.MustCompile(`(?:Review hint|Hint):\s*"([^"]*)"`)

// ExtractHint pulls the original hint out of a rendered .worktree.md body.
// Returns "" if no hint marker is present (e.g. "No hint provided" fallback).
func ExtractHint(content string) string {
	m := hintMarkerRegex.FindStringSubmatch(content)
	if m == nil {
		return ""
	}
	return m[1]
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
