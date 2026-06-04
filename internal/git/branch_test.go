package git

import "testing"

func TestMakeBranch(t *testing.T) {
	cases := []struct {
		kind, hint, want string
	}{
		{"task", "fix CU-86ca3yt48 location settings issues", "fix/CU-86ca3yt48/location-settings-issues"},
		{"task", "add e2e reusable workflow", "feat/e2e-reusable-workflow"},
		{"task", "refactor https://app.clickup.com/t/86c9hq28r foo bar", "refactor/CU-86c9hq28r/foo-bar"},
		{"task", "cleanup unused imports", "chore/unused-imports"},
		{"task", "docs update readme", "docs/update-readme"},
		{"review", "CU-86ca3yt48 pr review", "review/CU-86ca3yt48/pr-review"},
		{"task", "just a vague hint", "feat/just-a-vague-hint"},
	}
	for _, c := range cases {
		got := MakeBranch(c.kind, c.hint)
		if got != c.want {
			t.Errorf("MakeBranch(%q, %q) = %q, want %q", c.kind, c.hint, got, c.want)
		}
	}
}
