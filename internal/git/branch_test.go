package git

import "testing"

func TestMakeBranch(t *testing.T) {
	cases := []struct {
		kind, hint, want string
	}{
		// Per how-we-build/coding-guidelines/git-strategy.md (Conventional Branch):
		// branch prefixes are feature | fix | hotfix | epic | chore; id is `#<id>`.
		{"task", "fix CU-86ca3yt48 location settings issues", "fix/#86ca3yt48/location-settings-issues"},
		{"task", "add e2e reusable workflow", "feature/e2e-reusable-workflow"},
		// Refactors bucket into `chore` per SOP.
		{"task", "refactor https://app.clickup.com/t/86c9hq28r foo bar", "chore/#86c9hq28r/foo-bar"},
		{"task", "epic CU-86c00000 dashboard rebuild", "epic/#86c00000/dashboard-rebuild"},
		{"task", "hotfix CU-86cabc123 payments down", "hotfix/#86cabc123/payments-down"},
		// Internal / deps tasks bucket into `chore`. No `no-task` segment per
		// PR #18 — bare `chore/<desc>` when no id. The keyword that triggered
		// the prefix (`dependency`, `chore`, etc.) is stripped from the slug.
		{"task", "update firebase dependency", "chore/update-firebase"},
		{"task", "chore CU-86c11111 update firebase dependency", "chore/#86c11111/update-firebase-dependency"},
		{"review", "CU-86ca3yt48 pr review", "review/#86ca3yt48/pr-review"},
		{"task", "just a vague hint", "feature/just-a-vague-hint"},
	}
	for _, c := range cases {
		got := MakeBranch(c.kind, c.hint)
		if got != c.want {
			t.Errorf("MakeBranch(%q, %q) = %q, want %q", c.kind, c.hint, got, c.want)
		}
	}
}
