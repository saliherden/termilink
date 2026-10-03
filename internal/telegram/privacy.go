package telegram

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// operationNotPermitted is the kernel message behind a macOS TCC denial. It
// arrives as command output rather than a Go error, because the denied
// operation happens inside the shell, so it is matched against the captured
// output.
const operationNotPermitted = "Operation not permitted"

// guardedHomeFolders are the folders macOS protects with TCC. A process started
// by launchd has no access to them unless the user grants Full Disk Access,
// which is why `ls` in a project under ~/Desktop fails under the service but
// works in a terminal.
var guardedHomeFolders = []string{"Desktop", "Documents", "Downloads"}

// privacyProtectedDir reports whether dir is inside one of the folders macOS
// guards with TCC. The darwin flag is supplied explicitly so the path logic is
// testable on any OS: on other platforms those directories carry no special
// protection, and a non-absolute path is not classifiable at all.
func privacyProtectedDir(darwin bool, dir string) bool {
	if !darwin || !filepath.IsAbs(dir) {
		return false
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	clean := filepath.Clean(dir)
	for _, name := range guardedHomeFolders {
		guarded := filepath.Join(home, name)
		if clean == guarded || strings.HasPrefix(clean, guarded+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// permissionHint explains a macOS TCC denial to the owner. It returns "" unless
// the command output shows an EPERM and the working directory is in a guarded
// folder, so an unrelated "Operation not permitted" is never mislabelled as a
// macOS policy problem.
func permissionHint(cwd string, output []byte) string {
	return privacyHint(runtime.GOOS == "darwin", cwd, output)
}

func privacyHint(darwin bool, cwd string, output []byte) string {
	if !strings.Contains(string(output), operationNotPermitted) {
		return ""
	}
	if !privacyProtectedDir(darwin, cwd) {
		return ""
	}
	return "macOS is blocking the termilink service from reading ~/Desktop, " +
		"~/Documents and ~/Downloads. A background service has no access there " +
		"unless you grant it Full Disk Access, or you move the project out of those " +
		"folders (for example to ~/code) and reinstall the service."
}
