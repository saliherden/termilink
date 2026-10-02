// Package systemd renders a servicedef.Service as a systemd user unit for
// Linux. It is the second of the platform renderers and consumes exactly the
// same neutral description launchd does, so adding it changed nothing in the
// description itself.
package systemd

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/saliherden/termilink/internal/servicedef"
)

// FileName returns the unit file name for a label. systemd addresses a unit by
// its file name, so this name is the unit's identity rather than a label
// repeated inside it.
func FileName(label string) string { return label + ".service" }

// UnitPath returns where a user-scoped unit belongs.
//
// A user unit rather than a system one: the agent is a per-user gateway that
// reads the user's config.yaml and .env, drives the user's login shell and
// writes the user's session state, so it has no business running as root. A
// user unit is also the only kind a non-root install can manage without sudo.
func UnitPath(home, label string) string {
	return filepath.Join(home, ".config", "systemd", "user", FileName(label))
}

// LogProductDir is the directory name used for log files under ~/.local/state.
// Named for the product, matching launchd's choice, so the two platforms are
// recognisable as the same application.
const LogProductDir = "termilink"

// LogPaths returns the state directory and the two stream files.
//
// systemd would collect the output in the journal on its own, and
// `journalctl --user -u com.termilink.agent` is the idiomatic way to read it.
// The files exist anyway because the journal is not always available — a
// minimal user manager, a container — and because a single file is easier to
// hand to someone than a journal invocation. ~/.local/state is the XDG home
// for exactly this kind of runtime state, and it is not purged the way /tmp is.
func LogPaths(home, label string) servicedef.Paths {
	dir := filepath.Join(home, ".local", "state", LogProductDir)
	return servicedef.Paths{
		Definition: UnitPath(home, label),
		LogDir:     dir,
		LogStdout:  filepath.Join(dir, "stdout.log"),
		LogStderr:  filepath.Join(dir, "stderr.log"),
		User:       currentUser(),
	}
}

// currentUser returns the login name the user manager runs under.
func currentUser() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return ""
}

// Render produces the systemd unit for svc. paths supplies the filesystem
// locations, since those are the platform's business rather than the
// description's.
//
// The settings that matter are the same three as on macOS, expressed in a
// different grammar:
//
//   - Environment= carries the captured PATH. A user unit inherits almost
//     nothing, and the default PATH has no Homebrew, no version managers and no
//     language toolchain, so the agent's shell would fail to find the user's
//     tools while working perfectly in a terminal. Since internal/terminal and
//     internal/agent build a child environment from os.Environ(), the job's PATH
//     is the shell's PATH.
//   - Restart=on-failure with RestartSec bounds restarts. A job that fails on
//     startup is respawned in a tight loop without it, filling the log until the
//     real error is buried. on-failure, not always, so a clean exit stays
//     stopped instead of fighting an operator who stopped it.
//   - StandardOutput/StandardError are kept apart so "what did the agent say
//     before it died" is a single file to open.
func Render(svc *servicedef.Service, paths servicedef.Paths) ([]byte, error) {
	if svc == nil {
		return nil, fmt.Errorf("systemd: nil service")
	}
	if err := svc.Validate(); err != nil {
		return nil, err
	}
	if paths.Definition == "" {
		return nil, fmt.Errorf("systemd: no unit path given")
	}
	if paths.LogStdout == "" || paths.LogStderr == "" {
		return nil, fmt.Errorf("systemd: stdout and stderr log paths are required")
	}

	throttle := svc.ThrottleSeconds
	if throttle <= 0 {
		throttle = servicedef.DefaultThrottleSeconds
	}

	var b strings.Builder

	b.WriteString("[Unit]\n")
	b.WriteString("Description=TermiLink Telegram gateway\n")
	// A gateway needs the network, but the agent reconnects on its own, so this
	// only delays the first attempt; it is not a correctness requirement. No
	// Wants=, so a user manager that has no such target does not fail to queue
	// the unit over it.
	b.WriteString("After=network-online.target\n")

	b.WriteString("\n[Service]\n")
	b.WriteString("Type=simple\n")

	// ExecStart. The executable is absolute and the arguments follow as
	// separate words, so nothing depends on the job's PATH to find the binary.
	b.WriteString("ExecStart=")
	b.WriteString(unitEscape(svc.Executable))
	for _, arg := range svc.Args {
		b.WriteString(" ")
		b.WriteString(unitEscape(arg))
	}
	b.WriteString("\n")

	// WorkingDirectory. Not the home directory: the agent reads config.yaml and
	// .env from its working directory, so anything else starts the gateway with
	// no configuration at all.
	fmt.Fprintf(&b, "WorkingDirectory=%s\n", unitEscape(svc.WorkingDir))

	// Environment. TERM matters as much as PATH here: the agent relays a PTY,
	// and a shell without a terminal type makes prompts and cursor addressing
	// behave differently from the user's own terminal.
	for _, kv := range svc.Env {
		key, value, ok := strings.Cut(kv, "=")
		if !ok || key == "" {
			continue
		}
		fmt.Fprintf(&b, "Environment=%s=%s\n", key, unitEscape(value))
	}
	if !hasEnvKey(svc.Env, "TERM") {
		b.WriteString("Environment=TERM=xterm-256color\n")
	}

	b.WriteString("Restart=on-failure\n")
	fmt.Fprintf(&b, "RestartSec=%s\n", strconv.Itoa(throttle))

	b.WriteString("StandardOutput=append:" + unitEscape(paths.LogStdout) + "\n")
	b.WriteString("StandardError=append:" + unitEscape(paths.LogStderr) + "\n")

	// WantedBy is what makes `systemctl --user enable` link the unit into
	// default.target, which is what starts it at login.
	b.WriteString("\n[Install]\n")
	b.WriteString("WantedBy=default.target\n")

	return []byte(b.String()), nil
}

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

// unitEscape encodes a value so systemd reads it literally.
//
// Specifier expansion turns a bare % into a %-specifier, so a literal percent
// has to be doubled — otherwise a path with a "%" in it becomes an invalid
// specifier and the unit fails to load with an error that points at the line
// rather than the character. A value containing whitespace or a quote is
// wrapped in double quotes with its own quotes and backslashes escaped, which
// is what keeps a PATH with a space in it from becoming two assignments.
func unitEscape(s string) string {
	s = strings.ReplaceAll(s, "%", "%%")
	if !strings.ContainsAny(s, " \t\"'\\") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}
