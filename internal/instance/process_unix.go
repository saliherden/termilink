//go:build !windows

package instance

import "syscall"

// processAlive reports whether pid names a live process. Signal 0 runs the
// existence and permission checks without delivering anything, so an EPERM
// answer still proves the process is there — it just belongs to another user.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
