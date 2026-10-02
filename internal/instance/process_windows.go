//go:build windows

package instance

import "golang.org/x/sys/windows"

// stillActive is the exit code Windows reports for a process that has not
// terminated. The Win32 API documents it as STILL_ACTIVE, but
// golang.org/x/sys/windows does not export it.
const stillActive = 259

// processAlive reports whether pid names a live process. Windows has no signal
// 0, so the closest equivalent is to open the process and read its exit code:
// a running process reports STILL_ACTIVE, while a pid with no process behind it
// fails to open at all. The one false positive is a process that genuinely
// exited with code 259, which is rare enough not to be worth guarding against
// here — a stuck lock is recoverable, a boot loop is not.
func processAlive(pid int) bool {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)

	var code uint32
	if err := windows.GetExitCodeProcess(handle, &code); err != nil {
		return false
	}
	return code == stillActive
}
