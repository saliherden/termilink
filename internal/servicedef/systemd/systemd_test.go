package systemd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saliherden/termilink/internal/servicedef"
)

func goldenService() *servicedef.Service {
	return &servicedef.Service{
		Label:           "com.termilink.agent",
		Executable:      "/opt/termilink/bin/termilink",
		WorkingDir:      "/home/you/.termilink",
		Args:            []string{"start"},
		StateFile:       "/home/you/.termilink/state.json",
		LogDir:          "/home/you/.local/state/termilink",
		Env:             []string{"PATH=/opt/homebrew/bin:/usr/bin:/bin", "TERM=xterm-256color"},
		ThrottleSeconds: 10,
	}
}

func goldenPaths() servicedef.Paths {
	return servicedef.Paths{
		Definition: "/home/you/.config/systemd/user/com.termilink.agent.service",
		LogDir:     "/home/you/.local/state/termilink",
		LogStdout:  "/home/you/.local/state/termilink/stdout.log",
		LogStderr:  "/home/you/.local/state/termilink/stderr.log",
		User:       "you",
	}
}

// goldenUnit is the exact expected output for goldenService/goldenPaths. It is
// written out in full rather than assembled from fragments, so an unintended
// change to ordering, a stray blank line or a changed default shows up as a
// readable diff instead of a passing test.
const goldenUnit = `[Unit]
Description=TermiLink Telegram gateway
After=network-online.target

[Service]
Type=simple
ExecStart=/opt/termilink/bin/termilink start
WorkingDirectory=/home/you/.termilink
Environment=PATH=/opt/homebrew/bin:/usr/bin:/bin
Environment=TERM=xterm-256color
Restart=on-failure
RestartSec=10
StandardOutput=append:/home/you/.local/state/termilink/stdout.log
StandardError=append:/home/you/.local/state/termilink/stderr.log

[Install]
WantedBy=default.target
`

func mustRender(t *testing.T) []byte {
	t.Helper()
	def, err := Render(goldenService(), goldenPaths())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return def
}

func TestRenderMatchesGolden(t *testing.T) {
	if got := string(mustRender(t)); got != goldenUnit {
		t.Errorf("rendered unit does not match the golden file\n--- got ---\n%s\n--- want ---\n%s", got, goldenUnit)
	}
}

// TestRenderedUnitVerifiesWithSystemdAnalyze hands the unit to systemd's own
// parser, which is the only thing here that actually knows the unit grammar.
// The golden test pins what we intended; this catches the case where what we
// intended is not valid.
func TestRenderedUnitVerifiesWithSystemdAnalyze(t *testing.T) {
	bin, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skipf("systemd-analyze is unavailable: %v", err)
	}
	dir := t.TempDir()
	// verify refuses a unit whose ExecStart does not exist, so point it at a
	// real file rather than the golden path, which is only a name on paper.
	exe := filepath.Join(dir, "termilink")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	svc := goldenService()
	svc.Executable = exe
	def, err := Render(svc, goldenPaths())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, FileName(servicedef.DefaultLabel))
	if err := os.WriteFile(path, def, 0o644); err != nil {
		t.Fatalf("write unit: %v", err)
	}
	if out, err := exec.Command(bin, "verify", path).CombinedOutput(); err != nil {
		t.Fatalf("systemd-analyze verify rejected the unit: %v\n%s", err, out)
	}
}

func TestRenderAddsTermWhenMissing(t *testing.T) {
	svc := goldenService()
	svc.Env = []string{"PATH=/usr/bin:/bin"}
	def, err := Render(svc, goldenPaths())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(def), "Environment=TERM=xterm-256color\n") {
		t.Errorf("expected a default TERM:\n%s", def)
	}
}

func TestRenderKeepsAnExplicitTerm(t *testing.T) {
	svc := goldenService()
	svc.Env = []string{"TERM=screen"}
	def, err := Render(svc, goldenPaths())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(def), "Environment=TERM=screen\n") {
		t.Errorf("expected the explicit TERM to survive:\n%s", def)
	}
	if strings.Contains(string(def), "xterm-256color") {
		t.Errorf("default TERM must not override an explicit one:\n%s", def)
	}
}

func TestRenderCarriesEveryEnvEntry(t *testing.T) {
	svc := goldenService()
	svc.Env = []string{"PATH=/usr/bin:/bin", "GOPATH=/home/you/go", "EDITOR=vim"}
	def, err := Render(svc, goldenPaths())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Environment=PATH=/usr/bin:/bin\n",
		"Environment=GOPATH=/home/you/go\n",
		"Environment=EDITOR=vim\n",
	} {
		if !strings.Contains(string(def), want) {
			t.Errorf("missing %q in:\n%s", want, def)
		}
	}
}

// A PATH with a space in it would otherwise become two assignments, the second
// an invalid environment entry that makes systemd refuse the whole unit.
func TestRenderQuotesValuesWithSpaces(t *testing.T) {
	svc := goldenService()
	svc.Env = []string{`PATH=/opt/my tools/bin:/usr/bin`}
	def, err := Render(svc, goldenPaths())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(def), `Environment=PATH="/opt/my tools/bin:/usr/bin"`+"\n") {
		t.Errorf("a value with a space must be quoted:\n%s", def)
	}
}

// A literal percent in a path is a specifier to systemd, so it has to be
// doubled or the unit fails to load with an error pointing at the line rather
// than the character.
func TestRenderDoublesPercentForSpecifiers(t *testing.T) {
	svc := goldenService()
	svc.WorkingDir = "/home/you/100%/termilink"
	def, err := Render(svc, goldenPaths())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(def), "WorkingDirectory=/home/you/100%%/termilink\n") {
		t.Errorf("a literal percent must be doubled:\n%s", def)
	}
}

func TestRenderUsesTheDefaultThrottleWhenUnset(t *testing.T) {
	svc := goldenService()
	svc.ThrottleSeconds = 0
	def, err := Render(svc, goldenPaths())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(def), "RestartSec=10\n") {
		t.Errorf("expected the default throttle:\n%s", def)
	}
}

func TestRenderIncludesArgumentsInOrder(t *testing.T) {
	svc := goldenService()
	svc.Args = []string{"start", "--verbose"}
	def, err := Render(svc, goldenPaths())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(def), "ExecStart=/opt/termilink/bin/termilink start --verbose\n") {
		t.Errorf("arguments must follow the executable in order:\n%s", def)
	}
}

func TestRenderRejectsBadInput(t *testing.T) {
	t.Run("nil service", func(t *testing.T) {
		if _, err := Render(nil, goldenPaths()); err == nil {
			t.Error("expected an error for a nil service")
		}
	})

	t.Run("missing unit path", func(t *testing.T) {
		paths := goldenPaths()
		paths.Definition = ""
		if _, err := Render(goldenService(), paths); err == nil {
			t.Error("expected an error when no unit path is given")
		}
	})

	t.Run("missing log paths", func(t *testing.T) {
		paths := goldenPaths()
		paths.LogStdout = ""
		paths.LogStderr = ""
		if _, err := Render(goldenService(), paths); err == nil {
			t.Error("expected an error when the log paths are missing")
		}
	})

	t.Run("relative executable", func(t *testing.T) {
		svc := goldenService()
		svc.Executable = "termilink"
		if _, err := Render(svc, goldenPaths()); err == nil {
			t.Error("expected an error for a relative executable")
		}
	})
}

func TestPathsAndFileName(t *testing.T) {
	if got := FileName("com.termilink.agent"); got != "com.termilink.agent.service" {
		t.Errorf("FileName = %q", got)
	}
	// systemd looks for a user unit under ~/.config/systemd/user by file name.
	if got := UnitPath("/home/you", "com.termilink.agent"); got != "/home/you/.config/systemd/user/com.termilink.agent.service" {
		t.Errorf("UnitPath = %q", got)
	}
	paths := LogPaths("/home/you", "com.termilink.agent")
	if paths.Definition != "/home/you/.config/systemd/user/com.termilink.agent.service" {
		t.Errorf("Definition = %q", paths.Definition)
	}
	if paths.LogDir != "/home/you/.local/state/termilink" {
		t.Errorf("LogDir = %q", paths.LogDir)
	}
	if paths.LogStdout != "/home/you/.local/state/termilink/stdout.log" {
		t.Errorf("LogStdout = %q", paths.LogStdout)
	}
	if paths.LogStderr != "/home/you/.local/state/termilink/stderr.log" {
		t.Errorf("LogStderr = %q", paths.LogStderr)
	}
}
