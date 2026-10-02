//go:build darwin

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saliherden/termilink/internal/servicedef"
)

// macOS is the platform with a real implementation, so it is the one place the
// interface is checked against behaviour rather than against a refusal. The
// service was installed and run against this renderer during development; these
// tests pin the parts that would otherwise only be covered by doing that again.
func TestLaunchdRendererIsThePlatformRenderer(t *testing.T) {
	if _, ok := platformRenderer().(launchdRenderer); !ok {
		t.Errorf("platformRenderer() = %T, want launchdRenderer", platformRenderer())
	}
}

// supported must not refuse. The whole point of asking early is that the
// supported platform answers nil and keeps going.
func TestLaunchdRendererAcceptsWork(t *testing.T) {
	r := launchdRenderer{}
	if err := r.supported(); err != nil {
		t.Errorf("supported() = %v, want nil on macOS", err)
	}
	if got := r.name(); got != "launchd" {
		t.Errorf("name() = %q, want launchd", got)
	}
}

// The log directory is ~/Library/Logs/termilink, not /tmp: macOS purges /tmp
// periodically, and a service log that disappears on a schedule is worse than
// no log at all because it looks like silence rather than a gap.
func TestLaunchdRendererPathsAvoidTmp(t *testing.T) {
	r := launchdRenderer{}
	paths, err := r.paths(servicedef.DefaultLabel)
	if err != nil {
		t.Fatalf("paths: %v", err)
	}
	if strings.Contains(paths.LogDir, "/tmp") || strings.HasPrefix(paths.LogDir, "/var/folders") {
		t.Errorf("LogDir = %q, which macOS can purge", paths.LogDir)
	}
	if want := filepath.Join("Library", "Logs", "termilink"); !strings.HasSuffix(paths.LogDir, want) {
		t.Errorf("LogDir = %q, want it to end in %q", paths.LogDir, want)
	}
	if !strings.HasSuffix(paths.Plist, "Library/LaunchAgents/"+servicedef.DefaultLabel+".plist") {
		t.Errorf("Plist = %q, want a LaunchAgents plist for the default label", paths.Plist)
	}
	if paths.LogStdout == paths.LogStderr {
		t.Error("the two streams share a file, so they cannot be read apart")
	}
}

// render must produce something launchd will accept, and must not contain the
// token. The plist lives in a world-readable directory and is meant to be
// pasted into review, so a token in it is a leak in two directions.
func TestLaunchdRendererRendersAValidPlistWithoutTheToken(t *testing.T) {
	r := launchdRenderer{}
	paths, err := r.paths(servicedef.DefaultLabel)
	if err != nil {
		t.Fatal(err)
	}
	svc := servicedef.New(servicedef.DefaultLabel,
		"/usr/local/bin/termilink", "/Users/you/app", "/Users/you/.termilink/state.json",
		paths.LogDir, []string{"PATH=/usr/bin:/bin", "TERM=xterm-256color"})

	def, err := r.render(svc, paths)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.HasPrefix(string(def), "<?xml") {
		t.Error("render did not produce an XML document")
	}
	// plutil is the same parser launchd uses, so it is the check that matters.
	// A hand-rolled tag scan would pass on a plist launchd rejects.
	tmp := filepath.Join(t.TempDir(), "check.plist")
	if err := os.WriteFile(tmp, def, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("/usr/bin/plutil", "-lint", tmp).CombinedOutput()
	if err != nil {
		t.Errorf("launchd would reject the rendered plist: %v\n%s", err, out)
	}
	if strings.Contains(string(def), "123456:AAF") {
		t.Error("the rendered plist contains a bot token; it belongs in a 0600 .env, not a world-readable file")
	}
}
