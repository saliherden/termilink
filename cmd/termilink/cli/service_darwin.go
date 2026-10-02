//go:build darwin

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/saliherden/termilink/internal/servicedef"
	"github.com/saliherden/termilink/internal/servicedef/launchd"
)

// launchdRenderer installs the agent as a per-user launchd job.
//
// The methods are on a type rather than free functions so that this file and
// service_other.go satisfy the same interface; see serviceRenderer.
type launchdRenderer struct{}

// platformRenderer is the only place macOS identifies itself to the rest of the
// CLI, so there is exactly one answer to which renderer a build uses.
func platformRenderer() serviceRenderer { return launchdRenderer{} }

func (launchdRenderer) name() string { return "launchd" }

func (launchdRenderer) supported() error { return nil }

// paths resolves the LaunchAgents plist and the log files under
// ~/Library/Logs. macOS purges /tmp periodically, so a log written there
// disappears without a trace, which makes it useless for the one thing a service
// log is for.
func (launchdRenderer) paths(label string) (servicedef.Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return servicedef.Paths{}, fmt.Errorf("no home directory: a service definition needs absolute paths")
	}
	return launchd.LogPaths(home, label), nil
}

func (launchdRenderer) render(svc *servicedef.Service, paths servicedef.Paths) ([]byte, error) {
	return launchd.Render(svc, paths)
}

// installService writes the plist and registers it with launchd.
//
// The order matters. bootstrap loads the job from the plist, so the file has
// to be in place first; and the agent must not be left registered but unloaded
// if writing fails, so a partially written file is removed before returning.
func (launchdRenderer) install(svc *servicedef.Service, paths servicedef.Paths) error {
	if err := os.MkdirAll(filepath.Dir(paths.Plist), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(paths.Plist), err)
	}
	if err := os.MkdirAll(paths.LogDir, 0o700); err != nil {
		return fmt.Errorf("create the log directory %s: %w", paths.LogDir, err)
	}

	def, err := launchd.Render(svc, paths)
	if err != nil {
		return err
	}
	// 0644, not 0600: launchd reads the plist as the user, and a plist in
	// ~/Library/LaunchAgents is not a secret. It carries no token — the bot
	// token stays in config.yaml or a 0600 .env next to it.
	if err := os.WriteFile(paths.Plist, def, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", paths.Plist, err)
	}

	// bootout first, and ignore the error: a job that is already loaded is
	// exactly the case where the user is re-running install after an edit, and
	// bootout is how the stale definition gets replaced.
	_ = serviceBootout(svc, paths)

	if err := serviceBootstrap(svc, paths); err != nil {
		// Do not leave a plist that no supervisor knows about — the next
		// install would then have to guess whether it is active.
		_ = os.Remove(paths.Plist)
		return err
	}

	fmt.Printf("Installed %s\n", paths.Plist)
	fmt.Printf("Logs:   %s\n", paths.LogDir)
	fmt.Printf("Status: termilink service status\n")
	return nil
}

// uninstallService stops the job and removes the definition. The log directory
// is left in place: it is evidence of what the agent did, and deleting a
// service should not be the thing that destroys the record.
func (launchdRenderer) uninstall(svc *servicedef.Service, paths servicedef.Paths) error {
	stopped := serviceBootout(svc, paths)
	if err := os.Remove(paths.Plist); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", paths.Plist, err)
	}
	if stopped != nil {
		// The file is gone, which is what the user asked for; report the
		// supervisor problem rather than failing the whole command.
		fmt.Fprintf(os.Stderr, "warning: could not unload the running job: %v\n", stopped)
	}
	fmt.Printf("Removed %s\n", paths.Plist)
	fmt.Printf("Logs left in %s\n", paths.LogDir)
	return nil
}

// serviceStatus reports whether the job is registered and what it is doing.
func (launchdRenderer) status(svc *servicedef.Service, paths servicedef.Paths) error {
	if _, err := os.Stat(paths.Plist); err != nil {
		if os.IsNotExist(err) {
			fmt.Println("Not installed (no plist).")
			return nil
		}
		return fmt.Errorf("stat %s: %w", paths.Plist, err)
	}
	fmt.Printf("Installed: %s\n", paths.Plist)

	out, err := servicePrint(svc)
	if err != nil {
		// Not registered is a normal state, not a failure: the plist can be
		// present before the first login, or after an unload.
		fmt.Printf("Registered: no (%v)\n", err)
		return nil
	}
	fmt.Println("Registered: yes")
	for _, line := range interestingLaunchctlLines(out) {
		fmt.Printf("  %s\n", line)
	}
	return nil
}

// serviceBootstrap loads the job into the user's GUI domain.
//
// The domain is explicit because the modern launchctl requires one. The old
// `launchctl load` defaulted to it, which is why every older guide omits it and
// why the error from the modern form points at a missing path rather than a
// missing domain.
func serviceBootstrap(svc *servicedef.Service, paths servicedef.Paths) error {
	cmd := serviceCommand("bootstrap", "gui/"+strconv.Itoa(os.Getuid()), paths.Plist)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootstrap: %w\n%s", err, out)
	}
	return nil
}

// serviceBootout stops the job. It is safe to call when nothing is loaded,
// which matters because uninstall and re-install both call it.
func serviceBootout(svc *servicedef.Service, paths servicedef.Paths) error {
	cmd := serviceCommand("bootout", "gui/"+strconv.Itoa(os.Getuid())+"/"+svc.Label)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootout: %w\n%s", err, out)
	}
	return nil
}

// servicePrint returns launchctl's description of the job.
func servicePrint(svc *servicedef.Service) (string, error) {
	cmd := serviceCommand("print", "gui/"+strconv.Itoa(os.Getuid())+"/"+svc.Label)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// interestingLaunchctlLines keeps the few fields worth showing out of a
// several-hundred-line `launchctl print`. The pid and last exit status are the
// two that answer "is it running, and if not, what happened".
func interestingLaunchctlLines(out string) []string {
	wanted := map[string]bool{
		"state":          true,
		"pid":            true,
		"last exit code": true,
		"runs":           true,
	}
	var kept []string
	for _, line := range splitLines(out) {
		trimmed := trimSpace(line)
		key, _, ok := cutColon(trimmed)
		if !ok || !wanted[key] {
			continue
		}
		kept = append(kept, trimmed)
	}
	return kept
}
