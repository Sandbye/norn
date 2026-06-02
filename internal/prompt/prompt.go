package prompt

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"text/template"
	"time"

	"github.com/sandbye/work/internal/config"
)

//go:embed templates/*.md.tmpl
var embedded embed.FS

// Data holds all values available to templates.
type Data struct {
	Kind       string // "task" or "review"
	HintBlock  string
	User       config.User
	ClickUp    *config.ClickUp
	Verify     []string
	Setup      string
	Generated  string
}

// Render loads the template for the given kind and renders it with cfg + hint.
func Render(cfg config.Config, kind, hint string) (string, error) {
	tmplName := kind + ".md.tmpl"

	data := Data{
		Kind:      kind,
		HintBlock: hintBlock(kind, hint),
		User:      cfg.User,
		ClickUp:   cfg.ClickUp,
		Verify:    cfg.Verify,
		Setup:     cfg.Setup,
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
