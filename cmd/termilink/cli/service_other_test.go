//go:build !darwin

package cli

import (
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/saliherden/termilink/internal/servicedef"
)

// The refusal has to come before any work, and these tests exist because the
// previous shape could not express that. As build-tagged free functions, the
// unsupported platform still had to define renderService, so every caller
// checked `if err != nil` around a call that could never return nil on this
// platform — dead code that staticcheck reported and that a reader could not
// tell apart from a real check. The interface makes the call dynamic, so the
// check is honest everywhere.
//
// What that buys is a user on Linux who gets one line instead of a PATH lookup,
// a config load and a token warning before the same answer.
func TestUnsupportedRendererRefusesEveryEntryPoint(t *testing.T) {
	r := unsupportedRenderer{}
	svc := &servicedef.Service{Label: servicedef.DefaultLabel}
	paths := servicedef.Paths{}

	entryPoints := map[string]error{
		"supported": r.supported(),
		"paths":     r.pathsErr(svc.Label),
		"render":    r.renderErr(svc, paths),
		"install":   r.install(svc, paths),
		"uninstall": r.uninstall(svc, paths),
		"status":    r.status(svc, paths),
	}
	for name, err := range entryPoints {
		if err == nil {
			t.Errorf("%s returned nil on %s, which would read as a working install", name, runtime.GOOS)
			continue
		}
		// A bare "not implemented" leaves the user with nothing to do next. The
		// platform and the planned approach are the actionable part.
		if !strings.Contains(err.Error(), runtime.GOOS) {
			t.Errorf("%s error %q does not name the platform", name, err)
		}
	}
}

// paths returns two values, so the table above cannot call it directly. This
// keeps the table readable instead of wrapping every entry point by hand.
// paths and render return two values, so the table above cannot call them
// directly. These keep the table readable instead of wrapping every entry point
// by hand.
func (r unsupportedRenderer) pathsErr(label string) error {
	_, err := r.paths(label)
	return err
}

func (r unsupportedRenderer) renderErr(svc *servicedef.Service, paths servicedef.Paths) error {
	_, err := r.render(svc, paths)
	return err
}

func TestUnsupportedRendererSaysWhatIsPlanned(t *testing.T) {
	err := unsupportedRenderer{}.supported()
	if err == nil {
		t.Fatal("supported returned nil")
	}
	// The plan is named per platform because the work genuinely differs: a
	// user unit and a service registration are not the same task.
	want := map[string]string{
		"linux":   "systemd user unit",
		"windows": "service registration",
	}[runtime.GOOS]
	if want == "" {
		want = "native service definition"
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not mention the planned approach %q", err, want)
	}
}

// The name is used in `service status` output, so it has to identify the
// platform rather than repeat what an error already says.
func TestUnsupportedRendererNamesItsPlatform(t *testing.T) {
	r := unsupportedRenderer{}
	if got := r.name(); got != runtime.GOOS {
		t.Errorf("name() = %q, want %q", got, runtime.GOOS)
	}
}

// Every method refuses rather than returning a zero value. A Paths with empty
// fields would be the dangerous outcome: it renders to "" and the failure would
// surface later as a permission error on the filesystem instead of as the
// clear refusal this is.
func TestUnsupportedRendererNeverReturnsEmptyPaths(t *testing.T) {
	paths, err := unsupportedRenderer{}.paths(servicedef.DefaultLabel)
	if err == nil {
		t.Fatal("paths returned nil error, so an empty definition path would be used")
	}
	if paths != (servicedef.Paths{}) {
		t.Errorf("paths = %+v, want the zero value alongside the error", paths)
	}
}

// platformRenderer must hand back the refusing renderer on this platform. If it
// ever returned something else, every test above would be testing a type no
// user reaches.
func TestPlatformRendererIsTheUnsupportedOne(t *testing.T) {
	var r serviceRenderer = platformRenderer()
	if _, ok := r.(unsupportedRenderer); !ok {
		t.Errorf("platformRenderer() = %T, want unsupportedRenderer", r)
	}
	if err := r.supported(); err == nil {
		t.Error("the renderer the CLI uses accepts work on an unsupported platform")
	}
	if r.name() == "" {
		t.Error("name() is empty, so the refusal could not name its platform")
	}
}

// The refusal must be the first thing that happens, before the PATH lookup.
//
// resolveJobPath spawns an interactive login shell, so the previous shape made a
// Linux user wait through a zsh startup — and read a token warning — only to be
// told the platform was never going to work. This asserts the gate sits ahead of
// that work by pointing the command at a config that does not exist: if the
// refusal were still late, the error would be about the missing config instead.
func TestUnsupportedPlatformRefusesBeforeResolvingAnything(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "does-not-exist.yaml")

	for _, sub := range []string{"render", "install", "uninstall", "status"} {
		t.Run(sub, func(t *testing.T) {
			cmd := newServiceCmd(&cfg)
			cmd.SetArgs([]string{sub, "--path", "/usr/bin:/bin"})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)

			err := cmd.Execute()
			if err == nil {
				t.Fatalf("%s succeeded on an unsupported platform", sub)
			}
			if !strings.Contains(err.Error(), runtime.GOOS) {
				t.Errorf("%s error %q does not name the platform", sub, err)
			}
			if strings.Contains(err.Error(), "config") {
				t.Errorf("%s failed on the configuration before refusing: %v", sub, err)
			}
		})
	}
}

// `service path` stays available everywhere. Resolving a PATH is
// platform-independent — it already falls back from dscl to getent — so
// suppressing it would remove the one diagnostic that helps a Linux user work
// out why their shell has the tools and a future service job would not.
func TestServicePathWorksWithoutARenderer(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	cmd := newServiceCmd(&cfg)
	cmd.SetArgs([]string{"path", "--path", "/usr/bin:/bin"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	if err := cmd.Execute(); err != nil {
		t.Errorf("service path = %v, want it to work on every platform", err)
	}
}
