// Package task fetches candidate tasks from an external tracker (GitHub issues,
// ClickUp) so norn can seed a worktree at create time. It deliberately calls
// the tracker's CLI/REST directly — the launched agent has its own MCP for the
// deep work; norn only needs a title + id to name the branch and write the hint.
package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Task is a candidate pulled from a tracker.
type Task struct {
	ID          string // tracker id (issue number, ClickUp task id)
	Title       string
	URL         string
	Kind        string   // branch prefix hint: feature | fix | "" (unknown)
	Description string   // body / text content, baked into the worktree brief
	Group       string   // list / folder / space label, for grouping + filtering
	Labels      []string // tracker labels, for display in the task view
}

// Provider lists candidate tasks for the current repo/workspace.
type Provider interface {
	Name() string
	List(ctx context.Context, repoRoot string) ([]Task, error)
}

// GitHub lists open issues via the `gh` CLI (no token setup — reuses gh auth).
type GitHub struct{}

func (GitHub) Name() string { return "github" }

func (GitHub) List(ctx context.Context, repoRoot string) ([]Task, error) {
	cmd := exec.CommandContext(ctx, "gh", "issue", "list",
		"--json", "number,title,url,labels,body", "--limit", "50")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh issue list: %w", err)
	}
	var raw []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		URL    string `json:"url"`
		Body   string `json:"body"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse gh issues: %w", err)
	}
	tasks := make([]Task, 0, len(raw))
	for _, r := range raw {
		labels := make([]string, 0, len(r.Labels))
		for _, l := range r.Labels {
			labels = append(labels, l.Name)
		}
		tasks = append(tasks, Task{
			ID:          strconv.Itoa(r.Number),
			Title:       r.Title,
			URL:         r.URL,
			Kind:        kindFromLabels(r.Labels),
			Description: r.Body,
			Labels:      labels,
		})
	}
	return tasks, nil
}

// GitHubIssue fetches one issue by number for the repo at repoRoot. Unlike
// List it resolves owner/repo from the origin remote and passes it explicitly,
// so it also works against a bare mirror, where gh has no checkout to infer the
// repo from.
func GitHubIssue(ctx context.Context, repoRoot string, number int) (Task, error) {
	args := []string{"issue", "view", strconv.Itoa(number), "--json", "number,title,url,body,labels"}
	if nwo := originNWO(repoRoot); nwo != "" {
		args = append(args, "-R", nwo)
	}
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		if ee := new(exec.ExitError); errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return Task{}, fmt.Errorf("gh issue view %d: %s", number, strings.TrimSpace(string(ee.Stderr)))
		}
		return Task{}, fmt.Errorf("gh issue view %d: %w", number, err)
	}
	var raw struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		URL    string `json:"url"`
		Body   string `json:"body"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return Task{}, fmt.Errorf("parse gh issue: %w", err)
	}
	labels := make([]string, 0, len(raw.Labels))
	for _, l := range raw.Labels {
		labels = append(labels, l.Name)
	}
	return Task{
		ID:          strconv.Itoa(raw.Number),
		Title:       raw.Title,
		URL:         raw.URL,
		Kind:        kindFromLabels(raw.Labels),
		Description: raw.Body,
		Labels:      labels,
	}, nil
}

// originNWO reads "owner/repo" out of the origin remote URL. "" when there's no
// origin, or it isn't a remote URL we recognise (a local-path origin has no
// owner) — the caller then lets gh detect the repo itself.
func originNWO(repoRoot string) string {
	out, err := exec.Command("git", "-C", repoRoot, "config", "--get", "remote.origin.url").Output()
	if err != nil {
		return ""
	}
	raw := strings.TrimSpace(string(out))

	// Strip the transport + host, leaving the owner/repo path.
	var path string
	switch {
	case strings.Contains(raw, "://"):
		host := raw[strings.Index(raw, "://")+3:]
		i := strings.Index(host, "/")
		if i < 0 {
			return ""
		}
		path = host[i+1:]
	case strings.Contains(raw, ":"):
		// scp-style: git@github.com:owner/repo
		i := strings.Index(raw, ":")
		if !strings.Contains(raw[:i], "@") {
			return ""
		}
		path = raw[i+1:]
	default:
		return "" // local path clone — nothing to pass to gh
	}

	parts := strings.Split(strings.Trim(strings.TrimSuffix(strings.TrimSuffix(path, "/"), ".git"), "/"), "/")
	if len(parts) < 2 || parts[len(parts)-2] == "" || parts[len(parts)-1] == "" {
		return ""
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1]
}

func kindFromLabels(labels []struct {
	Name string `json:"name"`
}) string {
	for _, l := range labels {
		switch strings.ToLower(l.Name) {
		case "bug", "fix":
			return "fix"
		case "feature", "enhancement":
			return "feature"
		}
	}
	return ""
}

// ClickUp lists tasks from the workspace. With a Space/List scope it shows the
// tasks in that scope; unscoped it shows the tasks assigned to you (like `gh`).
type ClickUp struct {
	Token  string
	TeamID string // workspace id (resolved during `norn auth` if empty)
	Space  string // optional space scope
	ListID string // optional narrower list scope
}

func (ClickUp) Name() string { return "clickup" }

// NamedID is an id + display name (workspace/space/list) for auth pickers.
type NamedID struct {
	ID   string
	Name string
}

func clickupToken(token string) (string, error) {
	if token == "" {
		token = os.Getenv("CLICKUP_TOKEN")
	}
	if token == "" {
		return "", fmt.Errorf("no ClickUp token (set $CLICKUP_TOKEN or clickup.token)")
	}
	return token, nil
}

// clickupGET issues an authenticated GET and decodes the JSON body into v.
func clickupGET(ctx context.Context, token, url string, v any) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("Authorization", token)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("clickup request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("clickup api %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, v)
}

// ClickUpUser verifies a token and returns the authenticated username.
func ClickUpUser(ctx context.Context, token string) (string, error) {
	if token == "" {
		return "", fmt.Errorf("empty token")
	}
	var me struct {
		User struct {
			Username string `json:"username"`
		} `json:"user"`
	}
	if err := clickupGET(ctx, token, "https://api.clickup.com/api/v2/user", &me); err != nil {
		return "", err
	}
	return me.User.Username, nil
}

// ClickUpTeams lists the workspaces the token can see.
func ClickUpTeams(ctx context.Context, token string) ([]NamedID, error) {
	var data struct {
		Teams []NamedID `json:"teams"`
	}
	if err := clickupGET(ctx, token, "https://api.clickup.com/api/v2/team", &data); err != nil {
		return nil, err
	}
	return data.Teams, nil
}

// ClickUpSpaces lists the spaces in a workspace.
func ClickUpSpaces(ctx context.Context, token, teamID string) ([]NamedID, error) {
	var data struct {
		Spaces []NamedID `json:"spaces"`
	}
	url := fmt.Sprintf("https://api.clickup.com/api/v2/team/%s/space", teamID)
	if err := clickupGET(ctx, token, url, &data); err != nil {
		return nil, err
	}
	return data.Spaces, nil
}

// ClickUpLists lists the lists in a space (folderless + inside folders).
func ClickUpLists(ctx context.Context, token, spaceID string) ([]NamedID, error) {
	var out []NamedID
	var folderless struct {
		Lists []NamedID `json:"lists"`
	}
	if err := clickupGET(ctx, token, fmt.Sprintf("https://api.clickup.com/api/v2/space/%s/list", spaceID), &folderless); err != nil {
		return nil, err
	}
	out = append(out, folderless.Lists...)
	var folders struct {
		Folders []struct {
			Name  string    `json:"name"`
			Lists []NamedID `json:"lists"`
		} `json:"folders"`
	}
	if err := clickupGET(ctx, token, fmt.Sprintf("https://api.clickup.com/api/v2/space/%s/folder", spaceID), &folders); err == nil {
		for _, f := range folders.Folders {
			for _, l := range f.Lists {
				out = append(out, NamedID{ID: l.ID, Name: f.Name + " / " + l.Name})
			}
		}
	}
	return out, nil
}

func (c ClickUp) List(ctx context.Context, _ string) ([]Task, error) {
	token, err := clickupToken(c.Token)
	if err != nil {
		return nil, err
	}

	team := c.TeamID
	if team == "" {
		teams, err := ClickUpTeams(ctx, token)
		if err != nil {
			return nil, err
		}
		if len(teams) == 0 {
			return nil, fmt.Errorf("no ClickUp workspaces for this token")
		}
		team = teams[0].ID
	}

	// Build the filtered-team-tasks query. A chosen List/Space scopes it; with
	// no scope we fall back to the tasks assigned to you.
	q := "include_closed=false"
	switch {
	case c.ListID != "":
		q += "&list_ids[]=" + c.ListID
	case c.Space != "":
		q += "&space_ids[]=" + c.Space
	default:
		var me struct {
			User struct {
				ID int `json:"id"`
			} `json:"user"`
		}
		if err := clickupGET(ctx, token, "https://api.clickup.com/api/v2/user", &me); err != nil {
			return nil, err
		}
		q += "&assignees[]=" + strconv.Itoa(me.User.ID)
	}

	var data struct {
		Tasks []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			URL         string `json:"url"`
			TextContent string `json:"text_content"`
			Description string `json:"description"`
			List        struct {
				Name string `json:"name"`
			} `json:"list"`
			Folder struct {
				Name   string `json:"name"`
				Hidden bool   `json:"hidden"`
			} `json:"folder"`
		} `json:"tasks"`
	}
	url := fmt.Sprintf("https://api.clickup.com/api/v2/team/%s/task?%s", team, q)
	if err := clickupGET(ctx, token, url, &data); err != nil {
		return nil, err
	}
	tasks := make([]Task, 0, len(data.Tasks))
	for _, x := range data.Tasks {
		desc := x.TextContent
		if desc == "" {
			desc = x.Description
		}
		group := x.List.Name
		if x.Folder.Name != "" && !x.Folder.Hidden {
			group = x.Folder.Name + " / " + x.List.Name
		}
		tasks = append(tasks, Task{ID: x.ID, Title: x.Name, URL: x.URL, Description: desc, Group: group})
	}
	return tasks, nil
}
