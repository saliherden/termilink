// Package servicedef describes a TermiLink service installation in
// platform-neutral terms. Each platform's init system consumes the same
// description and renders it into its own native format — launchd plist on
// macOS, a systemd unit on Linux, a service registration on Windows.
//
// The description deliberately holds only what every platform needs. Anything
// specific to one init system is passed as a platform option at render time
// instead of being modelled here, so adding a platform does not mean reshaping
// this struct.
package servicedef

import (
	"fmt"
	"path/filepath"
	"strings"
)

// DefaultLabel is the reverse-DNS identifier used on every platform. Keeping
// it identical is what makes an install recognisable across systems.
const DefaultLabel = "com.termilink.agent"

// DefaultThrottleSeconds is how long a supervisor should wait before restarting
// a job that exited. Without it, a job that fails on startup immediately — a
// bad config path, a revoked token — is respawned in a tight loop that fills
// the disk with logs and buries the actual error. launchd's own default is
// 10s; the value is set explicitly so a platform without that default cannot
// inherit a worse one.
const DefaultThrottleSeconds = 10

// Service is a complete description of what to install and how it should run.
type Service struct {
	// Label identifies the job to the platform supervisor.
	Label string

	// Executable is the absolute path to the termilink binary. It is passed
	// as an argument rather than being discovered by the supervisor, since a
	// job started outside an interactive login has a minimal PATH and would
	// not find a version-manager shim.
	Executable string

	// WorkingDir is the directory the agent runs in. It must contain
	// config.yaml, and it is deliberately not the home directory: the agent
	// reads config.yaml and .env from the working directory, so pointing this
	// anywhere else starts the gateway with no configuration.
	WorkingDir string

	// Args are the arguments passed to Executable, not including argv[0].
	Args []string

	// StateFile is the absolute path of the persisted session state. A
	// supervisor often runs a job as an account with no usable home
	// directory, and without an explicit path the agent would start and then
	// forget every session on restart.
	StateFile string

	// LogDir is the absolute directory for stdout and stderr. It is the
	// platform's conventional log location, not /tmp: on macOS /tmp is
	// purged periodically, so a log written there simply disappears.
	LogDir string

	// Env is the environment handed to the job. PATH is always present.
	Env []string

	// ThrottleSeconds is the restart delay described by
	// DefaultThrottleSeconds.
	ThrottleSeconds int
}

// Paths is the platform-specific filesystem layout for a service install.
// macOS puts the plist in ~/Library/LaunchAgents and the logs in
// ~/Library/Logs; Linux writes a unit under ~/.config/systemd/user; Windows
// needs a directory for the wrapper script. Keeping this separate from
// Service is what lets the same description render everywhere.
type Paths struct {
	// Plist is the absolute path of the generated definition file.
	Plist string
	// LogDir is the absolute directory for stdout and stderr.
	LogDir string
	// LogStdout and LogStderr are the absolute file paths for each stream.
	// They are separate because keeping them apart is what makes
	// "what did the agent say before it died" a one-line grep.
	LogStdout string
	LogStderr string
	// User is the account the job runs as. Empty means the current user.
	User string
}

// Validate reports whether the description can be rendered. It runs before
// anything is written, so an incomplete description is refused at the call
// site rather than producing a job that cannot start.
func (s *Service) Validate() error {
	if s.Label == "" {
		return fmt.Errorf("service: label must not be empty")
	}
	if !filepath.IsAbs(s.Executable) {
		return fmt.Errorf("service: executable must be an absolute path (got %q)", s.Executable)
	}
	if !filepath.IsAbs(s.WorkingDir) {
		return fmt.Errorf("service: working directory must be an absolute path (got %q)", s.WorkingDir)
	}
	if s.StateFile != "" && !filepath.IsAbs(s.StateFile) {
		return fmt.Errorf("service: state file must be an absolute path (got %q)", s.StateFile)
	}
	for _, arg := range s.Args {
		if strings.ContainsAny(arg, "\n\x00") {
			return fmt.Errorf("service: argument contains a control character: %q", arg)
		}
	}
	return nil
}

// New builds a Service from the parts the CLI knows, filling in the defaults
// that every platform shares. Paths are the responsibility of the platform.
func New(label, executable, workingDir, stateFile, logDir string, env []string) *Service {
	throttle := DefaultThrottleSeconds
	return &Service{
		Label:           label,
		Executable:      executable,
		WorkingDir:      workingDir,
		Args:            []string{"start"},
		StateFile:       stateFile,
		LogDir:          logDir,
		Env:             env,
		ThrottleSeconds: throttle,
	}
}
