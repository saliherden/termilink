//go:build !darwin

package cli

import (
	"fmt"
	"runtime"

	"github.com/saliherden/termilink/internal/servicedef"
)

// unsupportedRenderer stands in on platforms that have no service installer yet.
//
// It exists because the command surface has to compile and be testable on every
// platform, and because "not implemented on linux" is a worse answer than it
// looks when the command first resolves a PATH, loads config.yaml and prints a
// token warning before failing. supported() is asked before any of that, so the
// refusal is the first thing printed.
//
// It was once a set of build-tagged free functions instead of a type. The
// callers had to check `if err != nil` around a call that could never return
// nil on this platform, which staticcheck correctly reported as dead code: the
// error was not reachable behaviour, it was decoration. Behind an interface the
// call is dynamic and the check means what it says on every platform.
//
// Implementing a platform means writing one type with these six methods and
// returning it from platformRenderer. Nothing in service.go changes.
type unsupportedRenderer struct{}

// platformRenderer is macOS's counterpart, and the only place this file touches
// the rest of the CLI.
func platformRenderer() serviceRenderer { return unsupportedRenderer{} }

func (unsupportedRenderer) name() string { return runtime.GOOS }

// supported is the refusal, asked before any work is done.
func (unsupportedRenderer) supported() error { return unsupported("service") }

// The remaining methods are unreachable in practice — supported has already
// refused — but they have to exist to satisfy the interface. They return the
// same error rather than nil so that a future caller reaching one directly gets
// a truthful answer instead of a zero Paths that would send a plist to "".
func (unsupportedRenderer) paths(string) (servicedef.Paths, error) {
	return servicedef.Paths{}, unsupported("service")
}

func (unsupportedRenderer) render(*servicedef.Service, servicedef.Paths) ([]byte, error) {
	return nil, unsupported("service")
}

func (unsupportedRenderer) install(*servicedef.Service, servicedef.Paths) error {
	return unsupported("service")
}

func (unsupportedRenderer) uninstall(*servicedef.Service, servicedef.Paths) error {
	return unsupported("service")
}

func (unsupportedRenderer) status(*servicedef.Service, servicedef.Paths) error {
	return unsupported("service")
}

// unsupported explains what is missing and where the plan lives, rather than
// failing with a bare "not implemented". The plan is named per platform because
// the approach genuinely differs: a user unit and a service registration are
// not the same amount of work.
func unsupported(what string) error {
	planned := map[string]string{
		"linux":   "a systemd user unit",
		"windows": "a service registration (nssm or the SCM directly)",
	}[runtime.GOOS]
	if planned == "" {
		planned = "a native service definition"
	}
	return fmt.Errorf("termilink service %s is not implemented on %s yet; the planned approach is %s",
		what, runtime.GOOS, planned)
}
