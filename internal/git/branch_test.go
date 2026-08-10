package git

import "testing"

func TestBranchLacksSlug(t *testing.T) {
	lacks := []string{
		"feature/#86caebh17", "fix/#86c9y34xd", "feature/20260701-084500", "chore/",
		// Dashboard format — id last, `CU-` prefixed.
		"feature/CU-86caebh17", "fix/86caebh17",
	}
	for _, b := range lacks {
		if !BranchLacksSlug(b) {
			t.Errorf("BranchLacksSlug(%q) = false, want true", b)
		}
	}
	has := []string{
		"fix/#86caebh17/no-show-bookings", "feature/social-login", "chore/#86c/update-deps",
		"feature/social-login/CU-86c41xu8m",
	}
	for _, b := range has {
		if BranchLacksSlug(b) {
			t.Errorf("BranchLacksSlug(%q) = true, want false", b)
		}
	}
}

func TestNormalizeSuggestedBranch(t *testing.T) {
	cases := []struct{ in, want string }{
		{"fix/#86caebh17/no-show-bookings-completed", "fix/#86caebh17/no-show-bookings-completed"},
		{"  `feature/social-login`  ", "feature/social-login"},
		{"Here you go: fix/payout-bug", ""},          // prose prefix → invalid
		{"FIX/#86C/Auto Payout Charge", "fix/#86c/auto-payout-charge"},
		{"random text", ""},                          // no valid prefix
		{"feature/foo\nbar baz", "feature/foo"},      // first line only
	}
	for _, c := range cases {
		if got := NormalizeSuggestedBranch(c.in); got != c.want {
			t.Errorf("NormalizeSuggestedBranch(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestComposeBranch(t *testing.T) {
	cases := []struct {
		name, format, prefix, id, title, want string
	}{
		// Dashboard: coding-guidelines/branch-naming/dashboard.md — id last, `CU-`.
		{"dashboard", "{prefix}/{title}/CU-{id}", "feature", "86c41xu8m", "social-login", "feature/social-login/CU-86c41xu8m"},
		{"dashboard no id", "{prefix}/{title}/CU-{id}", "chore", "", "update-firebase", "chore/update-firebase"},
		{"dashboard no title", "{prefix}/{title}/CU-{id}", "fix", "86c41xu9p", "", "fix/CU-86c41xu9p"},
		// Android / baseline: id in the middle segment.
		{"default", "", "fix", "86caebh17", "no-show-bookings", "fix/#86caebh17/no-show-bookings"},
		{"default no id", "", "feature", "", "social-login", "feature/social-login"},
	}
	for _, c := range cases {
		if got := ComposeBranch(c.format, c.prefix, c.id, c.title); got != c.want {
			t.Errorf("%s: ComposeBranch(%q, %q, %q, %q) = %q, want %q", c.name, c.format, c.prefix, c.id, c.title, got, c.want)
		}
	}
}

func TestSuggestedBranchParts(t *testing.T) {
	cases := []struct{ in, prefix, title string }{
		{"feature/social-login", "feature", "social-login"},
		// The model adds an id anyway — norn places ids, so drop it.
		{"fix/#86caebh17/no-show-bookings", "fix", "no-show-bookings"},
		{"feature/social-login/CU-86c41xu8m", "feature", "social-login"},
		{"random text", "", ""},
	}
	for _, c := range cases {
		prefix, title := SuggestedBranchParts(c.in)
		if prefix != c.prefix || title != c.title {
			t.Errorf("SuggestedBranchParts(%q) = (%q, %q), want (%q, %q)", c.in, prefix, title, c.prefix, c.title)
		}
	}
}

func TestMakeBranch(t *testing.T) {
	cases := []struct {
		kind, hint, want string
	}{
		// Default format (git-strategy.md baseline): prefixes are
		// feature | fix | hotfix | epic | chore; id is `#<id>` in the middle.
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
		got := MakeBranch(c.kind, c.hint, "")
		if got != c.want {
			t.Errorf("MakeBranch(%q, %q) = %q, want %q", c.kind, c.hint, got, c.want)
		}
	}
}

func TestMakeBranchDashboardFormat(t *testing.T) {
	const format = "{prefix}/{title}/CU-{id}"
	cases := []struct{ hint, want string }{
		{"fix CU-86ca3yt48 location settings issues", "fix/location-settings-issues/CU-86ca3yt48"},
		{"update firebase dependency", "chore/update-firebase"},
		{"https://app.clickup.com/t/86c9hq28r", "feature/CU-86c9hq28r"},
	}
	for _, c := range cases {
		if got := MakeBranch("task", c.hint, format); got != c.want {
			t.Errorf("MakeBranch(task, %q, dashboard) = %q, want %q", c.hint, got, c.want)
		}
	}
}
