package session

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Everything the agent writes under its own directory describes someone's work:
// the chat id of every conversation, and the path of every directory it has
// touched. On a shared machine a 0644 state file publishes all of that to every
// other account, which is why the audit log was already 0600 and this was not.
func TestStateFileIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits do not apply")
	}
	dir := filepath.Join(t.TempDir(), ".termilink")
	m := NewManagerWithStateFile(filepath.Join(dir, "state.json"))
	if _, err := m.Ensure("111"); err != nil {
		t.Fatal(err)
	}

	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	assertMode(t, filepath.Dir(m.StateFile()), 0o700)
	assertMode(t, m.StateFile(), 0o600)
}

// os.WriteFile only applies the mode when it creates the file, so the tightening
// has to survive a file that already exists at the old mode. This is the upgrade
// path for anyone who ran an earlier build.
func TestStateFileIsTightenedWhenItAlreadyExists(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits do not apply")
	}
	dir := filepath.Join(t.TempDir(), ".termilink")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := NewManagerWithStateFile(path)
	if _, err := m.Ensure("111"); err != nil {
		t.Fatal(err)
	}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	assertMode(t, m.StateFile(), 0o600)
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
