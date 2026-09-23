package instance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func lockPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "termilink.pid")
}

func TestAcquireRelease(t *testing.T) {
	path := lockPath(t)
	release, err := Acquire(path)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lock file missing: %v", err)
	}
	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("lock file not removed on release")
	}
}

func TestSecondAcquireFails(t *testing.T) {
	path := lockPath(t)
	release, err := Acquire(path)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer release()

	if _, err := Acquire(path); err == nil {
		t.Fatal("second acquire must fail while first is held")
	} else if !strings.Contains(err.Error(), "another instance is running") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStaleLockReclaimed(t *testing.T) {
	path := lockPath(t)
	if err := os.WriteFile(path, []byte("999999999\n"), 0o644); err != nil {
		t.Fatalf("seed stale lock: %v", err)
	}
	release, err := Acquire(path)
	if err != nil {
		t.Fatalf("acquire over stale lock: %v", err)
	}
	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}
}

func TestReleaseDoesNotRemoveForeignLock(t *testing.T) {
	path := lockPath(t)
	release, err := Acquire(path)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := os.WriteFile(path, []byte("1\n"), 0o644); err != nil {
		t.Fatalf("overwrite lock: %v", err)
	}
	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("release must not remove a foreign lock file")
	}
}
