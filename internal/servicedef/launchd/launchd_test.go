package launchd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saliherden/termilink/internal/servicedef"
)

// writeTemp writes content to a file in a temporary directory and returns its
// path, for handing the rendered plist to plutil.
func writeTemp(t *testing.T, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rendered.plist")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// goldenService returns a description with every field set, so the golden
// output below is a complete plist rather than a partial one.
func goldenService() *servicedef.Service {
	return &servicedef.Service{
		Label:      servicedef.DefaultLabel,
		Executable: "/usr/local/bin/termilink",
		WorkingDir: "/Users/you/termilink",
		Args:       []string{"start"},
		StateFile:  "/Users/you/.termilink/state.json",
		LogDir:     "/Users/you/Library/Logs/termilink",
		Env: []string{
			"PATH=/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin",
			"TERM=xterm-256color",
		},
		ThrottleSeconds: 10,
	}
}

func goldenPaths() servicedef.Paths {
	return servicedef.Paths{
		Plist:     "/Users/you/Library/LaunchAgents/com.termilink.agent.plist",
		LogDir:    "/Users/you/Library/Logs/termilink",
		LogStdout: "/Users/you/Library/Logs/termilink/stdout.log",
		LogStderr: "/Users/you/Library/Logs/termilink/stderr.log",
		User:      "you",
	}
}

// goldenPlist is the exact expected output for goldenService/goldenPaths. It
// is written out in full rather than assembled from fragments so that an
// unintended change to key order, indentation or a default shows up as a
// readable diff instead of a passing test.
const goldenPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.termilink.agent</string>
	<key>ProgramArguments</key>
	<array>
		<string>/usr/local/bin/termilink</string>
		<string>start</string>
	</array>
	<key>WorkingDirectory</key>
	<string>/Users/you/termilink</string>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
		<key>TERM</key>
		<string>xterm-256color</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ThrottleInterval</key>
	<integer>10</integer>
	<key>StandardOutPath</key>
	<string>/Users/you/Library/Logs/termilink/stdout.log</string>
	<key>StandardErrorPath</key>
	<string>/Users/you/Library/Logs/termilink/stderr.log</string>
	<key>ProcessType</key>
	<string>Background</string>
</dict>
</plist>
`

func TestRenderMatchesGolden(t *testing.T) {
	got, err := Render(goldenService(), goldenPaths())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if string(got) != goldenPlist {
		t.Errorf("rendered plist does not match the golden output.\n--- got ---\n%s\n--- want ---\n%s", got, goldenPlist)
	}
}

func TestRenderedPlistParsesWithPlutil(t *testing.T) {
	// A golden test only proves the output matches what a human wrote down; it
	// does not prove launchd can read it. plutil is the real parser, so it is
	// the one check that catches a malformed document.
	bin, err := exec.LookPath("plutil")
	if err != nil {
		t.Skipf("plutil is unavailable: %v", err)
	}
	out, err := Render(goldenService(), goldenPaths())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	path := writeTemp(t, out)
	if output, err := exec.Command(bin, "-lint", path).CombinedOutput(); err != nil {
		t.Fatalf("plutil rejected the plist: %v\n%s", err, output)
	}
}

func TestRenderAddsTermWhenMissing(t *testing.T) {
	// The agent relays a PTY, so a job with no terminal type makes prompts and
	// cursor addressing behave differently from the user's own terminal.
	svc := goldenService()
	svc.Env = []string{"PATH=/usr/bin:/bin"}
	out, err := Render(svc, goldenPaths())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(out), "<key>TERM</key>") {
		t.Error("TERM was not added when the environment lacked it")
	}
	if !strings.Contains(string(out), "xterm-256color") {
		t.Error("the default TERM value is missing")
	}
}

func TestRenderKeepsAnExplicitTerm(t *testing.T) {
	// An operator who set TERM deliberately — a Linux gateway reached over
	// SSH, say — must not have it overwritten.
	svc := goldenService()
	svc.Env = []string{"PATH=/usr/bin:/bin", "TERM=screen-256color"}
	out, err := Render(svc, goldenPaths())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(out), "screen-256color") {
		t.Error("an explicit TERM was not preserved")
	}
	if strings.Contains(string(out), "xterm-256color") {
		t.Error("the default TERM was written even though one was supplied")
	}
}

func TestRenderCarriesEveryEnvEntry(t *testing.T) {
	// The whole point of the captured PATH is that it reaches the job, so a
	// dropped entry means the agent's shell cannot find the tools the user
	// relies on.
	svc := goldenService()
	svc.Env = []string{"PATH=/opt/homebrew/bin:/usr/bin", "LANG=en_US.UTF-8", "TERM=xterm"}
	out, err := Render(svc, goldenPaths())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	text := string(out)
	for _, want := range []string{"<key>PATH</key>", "<key>LANG</key>", "en_US.UTF-8", "<key>TERM</key>"} {
		if !strings.Contains(text, want) {
			t.Errorf("the plist is missing %q:\n%s", want, text)
		}
	}
}

func TestRenderEscapesXMLCharacters(t *testing.T) {
	// A PATH entry containing "&" or "<" is unusual but legal, and a quoting
	// bug here yields a plist that fails to load with an error pointing nowhere
	// near the cause.
	svc := goldenService()
	svc.Env = []string{`PATH=/Users/you/R&D/bin:/opt/a<b>:/usr/bin`}
	out, err := Render(svc, goldenPaths())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	text := string(out)
	if !strings.Contains(text, "R&amp;D") {
		t.Errorf("an ampersand was not escaped:\n%s", text)
	}
	if !strings.Contains(text, "a&lt;b&gt;") {
		t.Errorf("angle brackets were not escaped:\n%s", text)
	}
	if strings.Contains(text, "/Users/you/R&D/bin") {
		t.Errorf("a raw ampersand reached the output:\n%s", text)
	}
}

func TestRenderUsesTheDefaultThrottleWhenUnset(t *testing.T) {
	svc := goldenService()
	svc.ThrottleSeconds = 0
	out, err := Render(svc, goldenPaths())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(out), "<key>ThrottleInterval</key>") {
		t.Fatal("ThrottleInterval is missing when the value is unset")
	}
	// Without this key a job that fails on startup is respawned in a tight
	// loop, filling the log until the real error is buried.
	if !strings.Contains(string(out), "10") {
		t.Errorf("the default throttle was not written:\n%s", out)
	}
}

func TestRenderIncludesArgumentsInOrder(t *testing.T) {
	svc := goldenService()
	svc.Args = []string{"start", "--config", "/Users/you/termilink/config.yaml"}
	out, err := Render(svc, goldenPaths())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	text := string(out)
	iExec := strings.Index(text, "<string>/usr/local/bin/termilink</string>")
	iStart := strings.Index(text, "<string>start</string>")
	iFlag := strings.Index(text, "<string>--config</string>")
	iPath := strings.Index(text, "<string>/Users/you/termilink/config.yaml</string>")
	if iExec < 0 || iStart < 0 || iFlag < 0 || iPath < 0 {
		t.Fatalf("not every argument reached the plist:\n%s", text)
	}
	if !(iExec < iStart && iStart < iFlag && iFlag < iPath) {
		t.Errorf("arguments are out of order:\n%s", text)
	}
}

func TestRenderRejectsBadInput(t *testing.T) {
	t.Run("nil service", func(t *testing.T) {
		if _, err := Render(nil, goldenPaths()); err == nil {
			t.Error("expected an error for a nil service")
		}
	})

	t.Run("missing plist path", func(t *testing.T) {
		paths := goldenPaths()
		paths.Plist = ""
		if _, err := Render(goldenService(), paths); err == nil {
			t.Error("expected an error when no plist path is given")
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
	if got := FileName("com.termilink.agent"); got != "com.termilink.agent.plist" {
		t.Errorf("FileName = %q", got)
	}
	// launchctl looks for the plist under ~/Library/LaunchAgents by name.
	if got := PlistPath("/Users/you", "com.termilink.agent"); got != "/Users/you/Library/LaunchAgents/com.termilink.agent.plist" {
		t.Errorf("PlistPath = %q", got)
	}
	paths := LogPaths("/Users/you", "com.termilink.agent")
	// The log directory is named for the product, not the label. They differ on
	// purpose, so this pins the difference: the plist is addressed by a
	// reverse-DNS label because launchd namespaces every vendor in one flat
	// space, while ~/Library/Logs holds human-facing directories that Apple
	// names after the product.
	if paths.Plist != "/Users/you/Library/LaunchAgents/com.termilink.agent.plist" {
		t.Errorf("Plist = %q", paths.Plist)
	}
	if paths.LogDir != "/Users/you/Library/Logs/termilink" {
		t.Errorf("LogDir = %q", paths.LogDir)
	}
	if paths.LogStdout != "/Users/you/Library/Logs/termilink/stdout.log" {
		t.Errorf("LogStdout = %q", paths.LogStdout)
	}
	if paths.LogStderr != "/Users/you/Library/Logs/termilink/stderr.log" {
		t.Errorf("LogStderr = %q", paths.LogStderr)
	}
	// /tmp is purged periodically on macOS, so a log written there is gone
	// without a trace. The log directory must not be it.
	if strings.HasPrefix(paths.LogDir, "/tmp") {
		t.Errorf("the log directory is under /tmp, which macOS purges: %q", paths.LogDir)
	}
}
