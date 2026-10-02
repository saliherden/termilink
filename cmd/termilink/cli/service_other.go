//go:build !darwin

package cli

import (
	"fmt"
	"runtime"

	"github.com/saliherden/termilink/internal/servicedef"
)

// The service renderer is macOS-only for now. These stubs keep the command
// surface identical on every platform, so the Linux CI runner compiles the same
// code path macOS gets and a cross-platform `termilink service install` is a
// matter of implementing this file rather than restructuring the command.
//
// The Linux and Windows work is tracked in project.md. It needs no new
// configuration: state_file and audit_keep are already in place, which is what
// a service install under an account with no home directory requires.

func renderService(*servicedef.Service, servicedef.Paths) ([]byte, error) {
	return nil, unsupported("render")
}

func installService(*servicedef.Service, servicedef.Paths) error {
	return unsupported("install")
}

func uninstallService(*servicedef.Service, servicedef.Paths) error {
	return unsupported("uninstall")
}

func serviceStatus(*servicedef.Service, servicedef.Paths) error {
	return unsupported("status")
}

// unsupported explains what is missing and where the plan lives, rather than
// failing with a bare "not implemented".
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
