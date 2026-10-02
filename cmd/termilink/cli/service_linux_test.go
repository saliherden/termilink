//go:build linux

package cli

import (
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saliherden/termilink/internal/servicedef"
)

// platformRenderer must hand back the systemd renderer on Linux. If it ever
// returned something else, every test below would be testing a type no user
// reaches.
func TestSystemdRendererIsThePlatformRenderer(t *testing.T) {
	var r serviceRenderer = platformRenderer()
	if _, ok := r.(systemdRenderer); !ok {
		t.Errorf("platformRenderer() = %T, want systemdRenderer", r)
	}
	if err := r.supported(); err != nil {
		t.Errorf("supported() = %v, want Linux to accept work", err)
	}
	if got := r.name(); got != "systemd" {
		t.Errorf("name() = %q, want systemd", got)
	}
}

// The unit and the logs both live under the home directory, and neither under
// /tmp, which is purged and would lose the log the user is trying to read.
func TestSystemdRendererPathsAvoidTmp(t *testing.T) {
	paths, err := systemdRenderer{}.paths(servicedef.DefaultLabel)
	if err != nil {
		t.Fatalf("paths: %v", err)
	}
	for name, p := range map[string]string{
		"Definition": paths.Definition,
		"LogDir":     paths.LogDir,
		"LogStdout":  paths.LogStdout,
		"LogStderr":  paths.LogStderr,
	} {
		if !filepath.IsAbs(p) {
			t.Errorf("%s = %q, want an absolute path", name, p)
		}
		if strings.HasPrefix(p, "/tmp") {
			t.Errorf("%s = %q, want it outside /tmp", name, p)
		}
	}
	if !strings.HasSuffix(paths.Definition, "/.config/systemd/user/"+servicedef.DefaultLabel+".service") {
		t.Errorf("Definition = %q, want a systemd user unit for the default label", paths.Definition)
	}
}

// The definition must not carry the bot token. It is world-readable, and the
// generated file is meant to be pasted into a review.
func TestSystemdRendererRendersAUnitWithoutTheToken(t *testing.T) {
	r := systemdRenderer{}
	logDir := "/home/you/.local/state/termilink"
	paths := servicedef.Paths{
		Definition: "/home/you/.config/systemd/user/" + servicedef.DefaultLabel + ".service",
		LogDir:     logDir,
		LogStdout:  logDir + "/stdout.log",
		LogStderr:  logDir + "/stderr.log",
	}
	svc := servicedef.New(servicedef.DefaultLabel, "/usr/local/bin/termilink",
		"/home/you/.termilink", "/home/you/.termilink/state.json", logDir,
		[]string{"PATH=/usr/bin:/bin"})

	def, err := r.render(svc, paths)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(string(def), "TELEGRAM_BOT_TOKEN") {
		t.Error("the unit must not carry a token")
	}
	if !strings.Contains(string(def), "ExecStart=/usr/local/bin/termilink start\n") {
		t.Errorf("unit is missing the ExecStart line:\n%s", def)
	}
	if !strings.Contains(string(def), "WantedBy=default.target\n") {
		t.Errorf("unit is missing the [Install] section that enable needs:\n%s", def)
	}
}

// `service path` is platform-independent, so it has to keep working on the
// platform that now has a renderer too.
func TestServicePathWorksOnLinux(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	cmd := newServiceCmd(&cfg)
	cmd.SetArgs([]string{"path", "--path", "/usr/bin:/bin"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	if err := cmd.Execute(); err != nil {
		t.Errorf("service path = %v, want it to work on every platform", err)
	}
}
