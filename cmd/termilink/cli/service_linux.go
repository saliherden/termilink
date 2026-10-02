//go:build linux

package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/saliherden/termilink/internal/servicedef"
	"github.com/saliherden/termilink/internal/servicedef/systemd"
)

// systemdRenderer installs the agent as a per-user systemd unit.
//
// The methods are on a type rather than free functions so that this file and
// service_darwin.go satisfy the same interface; see serviceRenderer.
type systemdRenderer struct{}

// platformRenderer is the only place Linux identifies itself to the rest of the
// CLI, so there is exactly one answer to which renderer a build uses.
func platformRenderer() serviceRenderer { return systemdRenderer{} }

func (systemdRenderer) name() string { return "systemd" }

// supported accepts on every Linux, including one without a running user
// manager: whether systemctl works is an environment question, and the failure
// is clearer as the systemctl error from install than as a refusal decided by a
// probe that may itself need systemctl to answer.
func (systemdRenderer) supported() error { return nil }

// paths resolves the user unit under ~/.config/systemd/user and the logs under
// ~/.local/state. Both are per-user XDG locations, which is what a user-scoped
// unit can write without root.
func (systemdRenderer) paths(label string) (servicedef.Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return servicedef.Paths{}, fmt.Errorf("no home directory: a service definition needs absolute paths")
	}
	return systemd.LogPaths(home, label), nil
}

func (systemdRenderer) render(svc *servicedef.Service, paths servicedef.Paths) ([]byte, error) {
	return systemd.Render(svc, paths)
}

// install writes the unit, reloads the manager and starts the service.
//
// The order matters: daemon-reload is what makes systemd notice a new or edited
// unit file, enable links it into default.target for the next login, and
// restart starts it now. restart rather than start because a re-install after
// an edit must replace the running process, and start would silently leave the
// old one in place. If the service will not start, the unit is disabled and
// removed rather than left behind enabled and broken.
func (systemdRenderer) install(svc *servicedef.Service, paths servicedef.Paths) error {
	if err := os.MkdirAll(filepath.Dir(paths.Definition), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(paths.Definition), err)
	}
	if err := os.MkdirAll(paths.LogDir, 0o700); err != nil {
		return fmt.Errorf("create the log directory %s: %w", paths.LogDir, err)
	}

	def, err := systemd.Render(svc, paths)
	if err != nil {
		return err
	}
	// 0644, not 0600: systemd reads the unit as the user, and a user unit is
	// not a secret. It carries no token — the bot token stays in config.yaml or
	// a 0600 .env next to it.
	if err := os.WriteFile(paths.Definition, def, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", paths.Definition, err)
	}

	unit := systemd.FileName(svc.Label)
	if err := systemctl("daemon-reload"); err != nil {
		return err
	}
	if err := systemctl("enable", unit); err != nil {
		return err
	}
	if err := systemctl("restart", unit); err != nil {
		// Do not leave a unit that is enabled but cannot start: the next login
		// would try it again and fail the same way, with less context.
		_ = systemctl("disable", unit)
		_ = os.Remove(paths.Definition)
		return err
	}

	fmt.Printf("Installed %s\n", paths.Definition)
	fmt.Printf("Logs:   %s\n", paths.LogDir)
	fmt.Printf("Status: termilink service status\n")
	return nil
}

// uninstall stops the unit and removes it, leaving the logs in place. The logs
// are evidence of what the agent did, and deleting a service should not be the
// thing that destroys the record.
func (systemdRenderer) uninstall(svc *servicedef.Service, paths servicedef.Paths) error {
	unit := systemd.FileName(svc.Label)
	stopped := systemctl("stop", unit)
	// disable is best-effort: a unit that was never enabled still has a file to
	// remove, and a missing symlink is not a reason to fail the uninstall.
	_ = systemctl("disable", unit)
	if err := os.Remove(paths.Definition); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", paths.Definition, err)
	}
	// Reload so the manager forgets the unit immediately rather than reporting
	// it as failed until the next reload.
	_ = systemctl("daemon-reload")
	if stopped != nil {
		// The file is gone, which is what the user asked for; report the
		// manager problem rather than failing the whole command.
		fmt.Fprintf(os.Stderr, "warning: could not stop the running service: %v\n", stopped)
	}
	fmt.Printf("Removed %s\n", paths.Definition)
	fmt.Printf("Logs left in %s\n", paths.LogDir)
	return nil
}

// status reports whether the unit exists and what systemd makes of it.
func (systemdRenderer) status(svc *servicedef.Service, paths servicedef.Paths) error {
	if _, err := os.Stat(paths.Definition); err != nil {
		if os.IsNotExist(err) {
			fmt.Println("Not installed (no unit file).")
			return nil
		}
		return fmt.Errorf("stat %s: %w", paths.Definition, err)
	}
	fmt.Printf("Installed: %s\n", paths.Definition)

	// show prints key=value lines and no pager, which is what makes the output
	// something this command can pass through instead of reformatting.
	out, err := systemctlOutput("show", systemd.FileName(svc.Label),
		"--property=ActiveState,SubState,MainPID,ExecMainStatus,NRestarts")
	if err != nil {
		// Not registered is a normal state, not a failure: the unit file can be
		// present before the first login, or after a disable.
		fmt.Printf("Registered: no (%v)\n", err)
		return nil
	}
	fmt.Println("Registered: yes")
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || !strings.Contains(trimmed, "=") {
			continue
		}
		fmt.Printf("  %s\n", trimmed)
	}
	return nil
}

// systemctl runs a systemctl --user subcommand, so every call goes through the
// user's own service manager rather than the system one.
func systemctl(args ...string) error {
	_, err := systemctlOutput(args...)
	return err
}

// systemctlOutput is systemctl with its combined output, which systemd puts the
// useful part of a failure in. --no-pager is added up front because several
// subcommands would otherwise block on a pager in an interactive terminal.
func systemctlOutput(args ...string) (string, error) {
	full := append([]string{"--user", "--no-pager"}, args...)
	out, err := exec.Command("systemctl", full...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("systemctl %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	return string(out), nil
}
