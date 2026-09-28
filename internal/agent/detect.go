package agent

import (
	"errors"
	"fmt"
	"os/exec"
)

// ErrAgentNotFound is returned when no supported coding-agent CLI can be found.
var ErrAgentNotFound = errors.New("agent: no coding agent CLI found")

// candidates is the auto-detection order for the interactive agent CLI.
var candidates = []string{"opencode", "claude", "codex", "gemini"}

// Resolve returns the path to the agent binary to run. command is either empty
// (auto-detect), a bare executable name, or an absolute/relative path. It
// verifies the binary exists so the caller fails cleanly before touching a PTY.
func Resolve(command string) (string, error) {
	if command != "" {
		p, err := exec.LookPath(command)
		if err != nil {
			return "", fmt.Errorf("agent: command %q not found: %w", command, err)
		}
		return p, nil
	}
	for _, c := range candidates {
		if p, err := exec.LookPath(c); err == nil {
			return p, nil
		}
	}
	return "", ErrAgentNotFound
}
