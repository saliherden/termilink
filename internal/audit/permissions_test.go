package audit

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The audit log already asked for 0600, but os.OpenFile only applies a mode when
// it creates the file. A log written by an earlier build therefore kept 0644
// while a new one got 0600 — the same log, with a different answer depending on
// when it happened to be created.
func TestAuditLogIsTightenedWhenItAlreadyExists(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits do not apply")
	}
	dir := filepath.Join(t.TempDir(), ".termilink")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "audit.log")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	l, err := OpenWithOptions(path, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()

	assertMode(t, dir, 0o700)
	assertMode(t, path, 0o600)
}

// Rotation renames the active log to .1 and creates a new one. The archive is a
// copy of a file that was private, so it has to stay private.
func TestRotatedArchiveStaysPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits do not apply")
	}
	dir := filepath.Join(t.TempDir(), ".termilink")
	path := filepath.Join(dir, "audit.log")
	l, err := OpenWithOptions(path, 16, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()

	for i := 0; i < 6; i++ {
		l.Audit(Entry{Action: "command", Cmd: "echo a reasonably long command"})
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Skipf("no archive produced: %v", err)
	}
	assertMode(t, path+".1", 0o600)
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
