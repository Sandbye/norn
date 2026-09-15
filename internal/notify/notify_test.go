package notify

import (
	"runtime"
	"strings"
	"testing"
)

// A branch name is user-controlled text that ends up inside an AppleScript
// string literal, so quoting it wrong is how a name like `fix/say-"hi"` stops
// being data and starts being script.
func TestAppleScriptQuotesTheInterpolatedText(t *testing.T) {
	cases := []struct {
		name, title, body, want string
	}{
		{"plain", "norn", "fix/rounding is waiting", `display notification "fix/rounding is waiting" with title "norn"`},
		{"quote in body", "norn", `fix/say-"hi"`, `display notification "fix/say-\"hi\"" with title "norn"`},
		{"backslash in body", "norn", `a\b`, `display notification "a\\b" with title "norn"`},
	}
	for _, c := range cases {
		if got := appleScript(c.title, c.body); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
	}
}

func TestDesktopCmdMatchesThePlatform(t *testing.T) {
	cmd := desktopCmd("norn", "waiting")
	switch runtime.GOOS {
	case "darwin":
		if cmd == nil || !strings.HasSuffix(cmd.Path, "osascript") {
			t.Fatalf("darwin: cmd = %v, want osascript", cmd)
		}
		if len(cmd.Args) != 3 || cmd.Args[1] != "-e" {
			t.Errorf("darwin: args = %v, want osascript -e <script>", cmd.Args)
		}
	case "linux":
		// notify-send is not installed on every runner, so nil is a valid
		// result here: the point is that it never picks the wrong helper.
		if cmd != nil && !strings.HasSuffix(cmd.Path, "notify-send") {
			t.Errorf("linux: cmd = %v, want notify-send or nil", cmd)
		}
	default:
		if cmd != nil {
			t.Errorf("%s: cmd = %v, want nil", runtime.GOOS, cmd)
		}
	}
}
