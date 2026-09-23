package instance

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// DefaultLockPath returns the global PID lock path shared by every TermiLink
// gateway instance (one Telegram gateway per token is allowed).
func DefaultLockPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".termilink", "termilink.pid")
}

// Acquire claims the single-instance lock at lockPath atomically. It returns a
// release func that removes the lock only if it still belongs to this process.
func Acquire(lockPath string) (func() error, error) {
	if lockPath == "" {
		return nil, errors.New("no lock path available")
	}
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			_, werr := fmt.Fprintf(f, "%d\n", os.Getpid())
			_ = f.Close()
			if werr != nil {
				_ = os.Remove(lockPath)
				return nil, werr
			}
			return func() error { return release(lockPath) }, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}

		pid, rerr := readPID(lockPath)
		if rerr != nil || !processAlive(pid) {
			_ = os.Remove(lockPath)
			continue
		}
		return nil, fmt.Errorf("another instance is running (pid %d)", pid)
	}
	return nil, errors.New("could not acquire instance lock")
}

func release(lockPath string) error {
	pid, err := readPID(lockPath)
	if err != nil {
		return err
	}
	if pid != os.Getpid() {
		return nil
	}
	if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func readPID(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, err
	}
	if pid <= 0 {
		return 0, errors.New("invalid pid")
	}
	return pid, nil
}

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
