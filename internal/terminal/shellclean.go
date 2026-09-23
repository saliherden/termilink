package terminal

import (
	"strings"
)

const shellSentinelLine = "TLMP>"

// CleanShellOutput removes TermiLink's internal framing artifacts from command
// output: the TLM_PWD cwd-marker line, the S_/E_/REQ_ frame markers and the
// TLMP> sentinel prompt. Everything else is preserved as-is.
func CleanShellOutput(out []byte) []byte {
	if len(out) == 0 {
		return out
	}
	lines := strings.Split(string(out), "\n")
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		l := strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(l)
		if trimmed == shellSentinelLine {
			continue
		}
		if strings.HasPrefix(line, "TLM_PWD:") {
			continue
		}
		if strings.HasPrefix(line, "S_"+shellMarkerPrefix) ||
			strings.HasPrefix(line, "E_"+shellMarkerPrefix) ||
			strings.HasPrefix(line, "REQ_"+shellMarkerPrefix) {
			continue
		}
		filtered = append(filtered, line)
	}
	return []byte(strings.Join(filtered, "\n"))
}
