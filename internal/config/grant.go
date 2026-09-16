package config

// What a reply is allowed to do, in norn's own words rather than any one
// agent's flag names.
//
// Every agent spells this differently (Claude Code has --permission-mode,
// Codex has --sandbox plus --ask-for-approval), so norn names the grant and
// each agent translates. Otherwise the config key and the UI would only make
// sense to whoever had that agent's docs open.

// Grant is how much authority an unattended reply carries.
type Grant string

const (
	// GrantAnswer changes nothing: the agent answers, and anything that would
	// need approval is refused. The default, because a reply is usually a word.
	GrantAnswer Grant = "answer"
	// GrantEdit lets it write files in the worktree, but not run commands.
	GrantEdit Grant = "edit"
	// GrantAct lets it run commands too, reviewed automatically rather than by
	// you. This is what a reply that should end in a commit needs.
	GrantAct Grant = "act"
)

// Grants are the valid values, weakest first, which is also the order the
// dashboard cycles them in.
var Grants = []Grant{GrantAnswer, GrantEdit, GrantAct}

// Label describes a grant by what it permits, not by what it is called.
func (g Grant) Label() string {
	switch g {
	case GrantEdit:
		return "may edit files"
	case GrantAct:
		return "may run commands, auto-reviewed"
	case GrantAnswer:
		return "answer only"
	}
	return string(g)
}

// Next cycles to the next grant. An unrecognized value cycles to the weakest
// rather than being treated as one of the known ones.
func (g Grant) Next() Grant {
	for i, v := range Grants {
		if v == g {
			return Grants[(i+1)%len(Grants)]
		}
	}
	return Grants[0]
}

// ReplyGrant is the configured starting grant, defaulting to the weakest. An
// unknown value in the config falls back rather than granting more than asked.
func (c Config) ReplyGrant() Grant {
	for _, v := range Grants {
		if Grant(c.ReplyPermissionMode) == v {
			return v
		}
	}
	return GrantAnswer
}
