package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureAndList(t *testing.T) {
	m := NewManager()
	s := m.Ensure("chat1")
	if s.ID != "chat1" {
		t.Fatalf("ID = %q", s.ID)
	}
	s.Cwd = "/tmp"
	if m.Ensure("chat1").Cwd != "/tmp" {
		t.Fatal("Ensure must return existing state")
	}
	rows := m.List()
	if len(rows) != 1 {
		t.Fatalf("List len = %d", len(rows))
	}
	if rows[0].Cwd != "/tmp" {
		t.Fatalf("cwd = %q", rows[0].Cwd)
	}
}

func TestStateFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	m := NewManagerWithStateFile(path)
	s := m.Ensure("chat1")
	s.Cwd = "/tmp/project"
	s.Project = "mobile"
	s.LastCmd = "ls"
	if err := m.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	m2 := NewManagerWithStateFile(path)
	got, ok := m2.Get("chat1")
	if !ok {
		t.Fatal("state not reloaded")
	}
	if got.Cwd != "/tmp/project" || got.LastCmd != "ls" || got.Project != "mobile" {
		t.Fatalf("reload mismatch: %+v", got)
	}
}

func TestRemove(t *testing.T) {
	m := NewManager()
	m.Ensure("a")
	m.Remove("a")
	if _, ok := m.Get("a"); ok {
		t.Fatal("expected removal")
	}
}

func TestRuntimeFieldsNotPersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	m := NewManagerWithStateFile(path)
	s := m.Ensure("chat1")
	m.SetActive("chat1", true)
	s.PID = 4242
	if err := m.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	m2 := NewManagerWithStateFile(path)
	got, ok := m2.Get("chat1")
	if !ok {
		t.Fatal("state not reloaded")
	}
	if m2.IsActive("chat1") {
		t.Fatal("the running flag must not survive a restart (stale lock bug)")
	}
	if got.PID != 0 {
		t.Fatalf("PID must not survive a restart, got %d", got.PID)
	}
}

// TestSaveReportsUnwritablePath is the regression for silent data loss: a state
// file the manager cannot create must come back as an error, not a no-op. A
// regular file standing where the directory belongs is used instead of a
// permission trick so the test works regardless of the user it runs as.
func TestSaveReportsUnwritablePath(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewManagerWithStateFile(filepath.Join(blocker, "nested", "state.json"))
	m.Ensure("chat1")

	if err := m.Save(); err == nil {
		t.Fatal("Save reported success for a state file it could not create")
	}
}

// TestSaveWithoutStateFileStaysQuiet keeps the memory-only manager working: a
// caller that never configured a state file has nothing to persist, so an error
// would be noise.
func TestSaveWithoutStateFileStaysQuiet(t *testing.T) {
	m := NewManager()
	m.Ensure("chat1")
	if err := m.Save(); err != nil {
		t.Fatalf("Save with no state file = %v, want nil", err)
	}
}
