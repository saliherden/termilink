//go:build darwin

package cli

import (
	"os/exec"
	"strings"
)

// serviceCommand builds a launchctl invocation. It exists so the argv is
// assembled in one place: the domain and label appear in bootstrap, bootout
// and print, and a mismatch between them is a confusing launchctl error.
func serviceCommand(args ...string) *exec.Cmd {
	return exec.Command("launchctl", args...)
}

// splitLines splits text into lines, tolerating CRLF.
func splitLines(s string) []string { return strings.Split(s, "\n") }

// trimSpace trims ASCII and Unicode whitespace from both ends.
func trimSpace(s string) string { return strings.TrimSpace(s) }

// cutColon splits a line at its first colon, which is the shape
// `launchctl print` uses for its key/value lines.
func cutColon(s string) (before, after string, found bool) {
	return strings.Cut(s, ":")
}
