package instance

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The pid file sits in the same directory as the audit log and the session
// state, and it is created the same way. It holds no secret, but a directory
// whose files are all private except one is a directory whose privacy depends on
// nobody looking at the exception.
func TestLockFileIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits do not apply")
	}
	dir := filepath.Join(t.TempDir(), ".termilink")
	path := filepath.Join(dir, "termilink.pid")

	release, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = release() })

	assertMode(t, dir, 0o700)
	assertMode(t, path, 0o600)
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s mode = %04o, want %04o", path, got, want)
	}
}
