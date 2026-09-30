package session

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestEnsureAndList(t *testing.T) {
	m := NewManager()
	s, err := m.Ensure("chat1")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if s.ID != "chat1" {
		t.Fatalf("ID = %q", s.ID)
	}
	if err := m.Mutate("chat1", func(s *State) { s.Cwd = "/tmp" }); err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	again, err := m.Ensure("chat1")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if again.Cwd != "/tmp" {
		t.Fatal("Ensure must return the existing state")
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
	if err := m.Mutate("chat1", func(s *State) {
		s.Cwd = "/tmp/project"
		s.Project = "mobile"
		s.LastCmd = "ls"
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}

	m2 := NewManagerWithStateFile(path)
	got, ok := m2.Snapshot("chat1")
	if !ok {
		t.Fatal("state not reloaded")
	}
	if got.Cwd != "/tmp/project" || got.LastCmd != "ls" || got.Project != "mobile" {
		t.Fatalf("reload mismatch: %+v", got)
	}
}

func TestRemove(t *testing.T) {
	m := NewManager()
	if _, err := m.Ensure("a"); err != nil {
		t.Fatal(err)
	}
	m.Remove("a")
	if _, ok := m.Snapshot("a"); ok {
		t.Fatal("expected removal")
	}
}

func TestRuntimeFieldsNotPersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	m := NewManagerWithStateFile(path)
	if _, err := m.Ensure("chat1"); err != nil {
		t.Fatal(err)
	}
	m.SetActive("chat1", true)
	if err := m.Mutate("chat1", func(s *State) { s.PID = 4242 }); err != nil {
		t.Fatal(err)
	}

	m2 := NewManagerWithStateFile(path)
	got, ok := m2.Snapshot("chat1")
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

// TestSnapshotHandsOutACopy is the regression for the live-pointer race. Get and
// Ensure used to return the *State stored in the map, so every caller read and
// wrote Cwd, Project and LastCmd with the manager's lock already released — while
// another goroutine was doing the same. A caller that writes to what Snapshot
// returned must not reach the manager.
func TestSnapshotHandsOutACopy(t *testing.T) {
	m := NewManager()
	if _, err := m.Ensure("chat1"); err != nil {
		t.Fatal(err)
	}
	s, ok := m.Snapshot("chat1")
	if !ok {
		t.Fatal("session missing")
	}
	s.Cwd = "/somewhere/else"
	s.Project = "hijacked"

	fresh, _ := m.Snapshot("chat1")
	if fresh.Cwd != "" || fresh.Project != "" {
		t.Fatalf("writing to a snapshot changed the manager: %+v", fresh)
	}
}

// TestConcurrentSnapshotAndMutate drives both paths from many goroutines. Under
// the old pointer-returning API the same loop raced on every field access; with
// -race it reported the write to a shared State while a reader was walking it.
// The assertion is deliberately weak — any single field may end up as any legal
// value — the point is that nothing tears and the pair stays coherent.
func TestConcurrentSnapshotAndMutate(t *testing.T) {
	m := NewManagerWithStateFile(filepath.Join(t.TempDir(), "state.json"))
	if _, err := m.Ensure("chat1"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = m.Mutate("chat1", func(s *State) {
					s.Cwd = "/a"
					s.Project = "p"
					s.LastCmd = "ls"
				})
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				s, ok := m.Snapshot("chat1")
				if !ok {
					t.Error("session vanished")
					return
				}
				// Cwd and Project are only ever written together, so a reader
				// that sees one without the other caught a torn update.
				if (s.Cwd == "") != (s.Project == "") {
					t.Errorf("torn read: %+v", s)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// TestEnsureReportsUnwritablePath is the regression for silent data loss on the
// create path. Ensure used to call saveLocked and throw the error away, so the
// first message from a chat created a session that no restart would ever bring
// back — with nothing in the log to say so. A regular file standing where the
// directory belongs is used instead of a permission trick so the test works
// regardless of the user it runs as.
func TestEnsureReportsUnwritablePath(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewManagerWithStateFile(filepath.Join(blocker, "nested", "state.json"))

	s, err := m.Ensure("chat1")
	if err == nil {
		t.Fatal("Ensure reported success for a state file it could not create")
	}
	// The session is still usable in memory — losing the binding across a
	// restart is recoverable, refusing the message that created it is not.
	if s.ID != "chat1" {
		t.Fatalf("Ensure must still return the new state, got %+v", s)
	}
}

// TestMutateReportsUnwritablePath pins the same reporting on the write path, and
// the memory must keep the change either way: the gateway keeps running, so
// /status and the next command must see the new cwd even though the file could
// not be written.
func TestMutateReportsUnwritablePath(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewManagerWithStateFile(filepath.Join(blocker, "nested", "state.json"))

	err := m.Mutate("chat1", func(s *State) { s.Cwd = "/tmp/project" })
	if err == nil {
		t.Fatal("Mutate reported success for a state file it could not create")
	}
	if got, _ := m.Snapshot("chat1"); got.Cwd != "/tmp/project" {
		t.Fatalf("in-memory state lost after a failed write: %+v", got)
	}
}

// TestSaveReportsUnwritablePath covers the explicit Save call.
func TestSaveReportsUnwritablePath(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewManagerWithStateFile(filepath.Join(blocker, "nested", "state.json"))
	if _, err := m.Ensure("chat1"); err == nil {
		t.Fatal("Ensure reported success for a state file it could not create")
	}

	if err := m.Save(); err == nil {
		t.Fatal("Save reported success for a state file it could not create")
	}
}

// TestSaveWithoutStateFileStaysQuiet keeps the memory-only manager working: a
// caller that never configured a state file has nothing to persist, so an error
// would be noise.
func TestSaveWithoutStateFileStaysQuiet(t *testing.T) {
	m := NewManager()
	if _, err := m.Ensure("chat1"); err != nil {
		t.Fatalf("Ensure with no state file = %v, want nil", err)
	}
	if err := m.Save(); err != nil {
		t.Fatalf("Save with no state file = %v, want nil", err)
	}
}
