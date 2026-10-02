// Package launchd renders a servicedef.Service as a launchd property list for
// macOS. It is the first of the platform renderers; systemd and Windows
// consume the same description.
package launchd

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/saliherden/termilink/internal/servicedef"
)

// FileName returns the plist file name for a label, which is launchd's own
// convention and what `launchctl` expects to find.
func FileName(label string) string { return label + ".plist" }

// PlistPath returns where the plist belongs for a user-scoped agent.
//
// The file name is the label and the label is reverse-DNS, because both are a
// system contract rather than a choice: launchd addresses a job by label, and it
// keeps every vendor's jobs in one flat namespace, so "termilink.agent" could
// collide with another product's idea of the same name and `com.termilink.agent`
// does not.
func PlistPath(home, label string) string {
	return filepath.Join(home, "Library", "LaunchAgents", FileName(label))
}

// LogProductDir is the directory name used for log files under ~/Library/Logs.
//
// The product name, not the label, and the difference is the whole point. Apple
// names a log directory after the thing that wrote it — SiriTTSService,
// WindowServer, Xsan — and uses reverse-DNS only where the system has to address
// something unambiguously, which is the plist and nothing else here. Grouping by
// product also survives a second service: this one logs to
// ~/Library/Logs/termilink, and a future runner logs beside it rather than into
// a parallel tree of com.termilink.* directories.
const LogProductDir = "termilink"

// LogPaths returns the platform-conventional log directory and the two stream
// files. The directory is ~/Library/Logs rather than /tmp because macOS purges
// /tmp periodically: a log written there disappears without a trace, which
// makes it useless for the one thing a service log is for.
func LogPaths(home, label string) servicedef.Paths {
	dir := filepath.Join(home, "Library", "Logs", LogProductDir)
	return servicedef.Paths{
		Definition: PlistPath(home, label),
		LogDir:     dir,
		LogStdout:  filepath.Join(dir, "stdout.log"),
		LogStderr:  filepath.Join(dir, "stderr.log"),
		User:       currentUser(),
	}
}

// currentUser returns the login name of the user running the install, which is
// what a per-user LaunchAgent is registered under.
func currentUser() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return ""
}

// Render produces the plist for svc. paths supplies the filesystem locations,
// since those are the platform's business rather than the description's.
//
// The two keys that differ from the old hand-written template are the reason
// this is generated at all:
//
//   - EnvironmentVariables carries the captured PATH. A launchd job gets
//     PATH=/usr/bin:/bin:/usr/sbin:/sbin, which contains no Homebrew, no nvm
//     and no language toolchain — so the agent's shell would fail to find gh,
//     node or java, and it would do so while working perfectly in a terminal.
//     Since internal/terminal and internal/agent both build a child
//     environment from os.Environ(), the job's PATH is the shell's PATH.
//   - ThrottleInterval bounds restarts. Without it a job that fails on startup
//     is respawned in a tight loop, filling the log until the real error is
//     buried under thousands of identical lines.
func Render(svc *servicedef.Service, paths servicedef.Paths) ([]byte, error) {
	if svc == nil {
		return nil, fmt.Errorf("launchd: nil service")
	}
	if err := svc.Validate(); err != nil {
		return nil, err
	}
	if paths.Definition == "" {
		return nil, fmt.Errorf("launchd: no plist path given")
	}
	if paths.LogStdout == "" || paths.LogStderr == "" {
		return nil, fmt.Errorf("launchd: stdout and stderr log paths are required")
	}

	throttle := svc.ThrottleSeconds
	if throttle <= 0 {
		throttle = servicedef.DefaultThrottleSeconds
	}

	var b strings.Builder
	b.WriteString(plistHeader)

	// Label
	b.WriteString("\t<key>Label</key>\n")
	fmt.Fprintf(&b, "\t<string>%s</string>\n", escape(svc.Label))

	// ProgramArguments. The executable is absolute and the subcommand follows
	// as separate entries, so nothing depends on the job's PATH to find it.
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	fmt.Fprintf(&b, "\t\t<string>%s</string>\n", escape(svc.Executable))
	for _, arg := range svc.Args {
		fmt.Fprintf(&b, "\t\t<string>%s</string>\n", escape(arg))
	}
	b.WriteString("\t</array>\n")

	// WorkingDirectory. Not the home directory: the agent reads config.yaml and
	// .env from its working directory, so anything else starts the gateway with
	// no configuration at all.
	b.WriteString("\t<key>WorkingDirectory</key>\n")
	fmt.Fprintf(&b, "\t<string>%s</string>\n", escape(svc.WorkingDir))

	// EnvironmentVariables. TERM matters as much as PATH here: the agent
	// relays a PTY, and a shell without a terminal type makes prompts and
	// cursor addressing behave differently from the user's own terminal.
	b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
	for _, kv := range svc.Env {
		key, value, ok := strings.Cut(kv, "=")
		if !ok || key == "" {
			continue
		}
		fmt.Fprintf(&b, "\t\t<key>%s</key>\n", escape(key))
		fmt.Fprintf(&b, "\t\t<string>%s</string>\n", escape(value))
	}
	if !hasEnvKey(svc.Env, "TERM") {
		b.WriteString("\t\t<key>TERM</key>\n\t\t<string>xterm-256color</string>\n")
	}
	b.WriteString("\t</dict>\n")

	// RunAtLoad starts the agent at login.
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")

	// KeepAlive restarts a crashed job but leaves a clean exit alone: an agent
	// stopped with `termilink stop` must stay stopped, not fight the operator
	// on every logout.
	b.WriteString("\t<key>KeepAlive</key>\n\t<dict>\n")
	b.WriteString("\t\t<key>SuccessfulExit</key>\n\t\t<false/>\n")
	b.WriteString("\t</dict>\n")

	fmt.Fprintf(&b, "\t<key>ThrottleInterval</key>\n\t<integer>%s</integer>\n", strconv.Itoa(throttle))

	// StandardOutPath and StandardErrorPath are kept apart so "what did the
	// agent say before it died" is a single file to open. Both live under
	// ~/Library/Logs, which macOS does not purge.
	b.WriteString("\t<key>StandardOutPath</key>\n")
	fmt.Fprintf(&b, "\t<string>%s</string>\n", escape(paths.LogStdout))
	b.WriteString("\t<key>StandardErrorPath</key>\n")
	fmt.Fprintf(&b, "\t<string>%s</string>\n", escape(paths.LogStderr))

	// ProcessType. Background is the correct classification for a daemon: it
	// keeps the agent from inheriting the GUI session's resource and
	// App Nap treatment, which can otherwise throttle a process that is
	// supposed to be always on.
	b.WriteString("\t<key>ProcessType</key>\n\t<string>Background</string>\n")

	b.WriteString(plistFooter)
	return []byte(b.String()), nil
}

const plistHeader = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`

const plistFooter = `</dict>
</plist>
`

// hasEnvKey reports whether the environment already defines key.
func hasEnvKey(env []string, key string) bool {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return true
		}
	}
	return false
}

// escape encodes the characters that are special inside an XML plist string.
// The paths that reach here are absolute filesystem paths and captured
// environment values, and both can legitimately contain an ampersand or an
// angle bracket — a PATH entry with an "&" in it is unusual but a quoting bug
// here produces a plist that fails to load with an error that points nowhere
// near the real cause.
func escape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return r.Replace(s)
}
