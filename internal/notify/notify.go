// Package notify pings the user when a thread needs them. The dashboard shows
// which thread is waiting, but only while it is on screen: with many parallel
// threads the point is to be told, not to watch.
//
// Two channels, because neither is reliable alone. The terminal bell always
// works but is invisible in a muted terminal; a desktop notification is visible
// but needs a helper binary and, on macOS, notification permission for the
// terminal app. Both are best-effort and silent on failure.
package notify

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// Notify fires a bell and, where possible, a desktop notification. It returns
// immediately: the desktop helper is spawned in the background so a slow or
// blocked one can never stall the caller's event loop.
func Notify(title, body string) {
	bell()
	if cmd := desktopCmd(title, body); cmd != nil {
		go func() { _ = cmd.Run() }()
	}
}

// bell goes to stderr, not stdout: stdout is the TUI's surface, and BEL is a
// terminal-level signal that does not belong in the rendered frame.
func bell() { fmt.Fprint(os.Stderr, "\a") }

// desktopCmd builds the platform's notification command, or nil when the
// platform has none or its helper is not installed.
func desktopCmd(title, body string) *exec.Cmd {
	switch runtime.GOOS {
	case "darwin":
		if _, err := exec.LookPath("osascript"); err != nil {
			return nil
		}
		return exec.Command("osascript", "-e", appleScript(title, body))
	case "linux":
		if _, err := exec.LookPath("notify-send"); err != nil {
			return nil
		}
		return exec.Command("notify-send", title, body)
	}
	return nil
}

// appleScript builds the `display notification` one-liner. Both fields are
// interpolated into AppleScript string literals, so a branch name carrying a
// quote or a backslash would otherwise break the script or, worse, extend it.
func appleScript(title, body string) string {
	return fmt.Sprintf("display notification %s with title %s", quote(body), quote(title))
}

func quote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
