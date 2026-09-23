package session

import (
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
	m.Save()

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
	s.Active = true
	s.PID = 4242
	m.Save()

	m2 := NewManagerWithStateFile(path)
	got, ok := m2.Get("chat1")
	if !ok {
		t.Fatal("state not reloaded")
	}
	if got.Active {
		t.Fatal("Active must not survive a restart (stale lock bug)")
	}
	if got.PID != 0 {
		t.Fatalf("PID must not survive a restart, got %d", got.PID)
	}
}
