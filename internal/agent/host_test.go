package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixtureAgent writes a small TUI-like shell script that prompts, reads a line
// and echoes it back, twice, then stays alive until killed. It exercises the
// PTY read/write path without depending on a real agent binary.
func fixtureAgent(t *testing.T) string {
	t.Helper()
	script := `#!/bin/sh
printf 'ready> '
read -r line
printf 'echo:%s> ' "$line"
read -r line
printf 'echo:%s\n' "$line"
sleep 600
`
	path := filepath.Join(t.TempDir(), "agent.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func waitFrame(t *testing.T, s *Session, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if strings.Contains(s.Frame(), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %q, frame=%q", want, s.Frame())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestStartEmptyCommand(t *testing.T) {
	if _, err := Start(nil, t.TempDir(), nil, nil); err == nil {
		t.Fatal("empty command must error")
	}
	if _, err := Start([]string{""}, t.TempDir(), nil, nil); err == nil {
		t.Fatal("blank command must error")
	}
}

func TestSessionStartWriteSniff(t *testing.T) {
	bin := fixtureAgent(t)
	dir := t.TempDir()
	s, err := Start([]string{bin}, dir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if !s.IsAlive() {
		t.Fatal("session not alive")
	}
	if s.PID() <= 0 {
		t.Fatalf("bad pid %d", s.PID())
	}
	if s.Dir() != dir {
		t.Fatalf("dir = %q, want %q", s.Dir(), dir)
	}
	if s.StartTime().After(time.Now()) {
		t.Fatal("start time in the future")
	}

	waitFrame(t, s, "ready>")

	if err := s.Write([]byte("hello world\n")); err != nil {
		t.Fatal(err)
	}
	waitFrame(t, s, "echo:hello world")

	if err := s.Write([]byte("second\n")); err != nil {
		t.Fatal(err)
	}
	waitFrame(t, s, "echo:second")
}

func TestSessionWriteBeforePrompt(t *testing.T) {
	bin := fixtureAgent(t)
	s, err := Start([]string{bin}, t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// Writing before the prompt is legal; the PTY buffers it.
	if err := s.Write([]byte("early\n")); err != nil {
		t.Fatal(err)
	}
	waitFrame(t, s, "echo:early")
}

func TestSessionSniffDirtyFlag(t *testing.T) {
	bin := fixtureAgent(t)
	s, err := Start([]string{bin}, t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	waitFrame(t, s, "ready>")

	_, changed := s.Sniff()
	if !changed {
		t.Fatal("initial frame must be reported as changed")
	}
	_, changed = s.Sniff()
	if changed {
		t.Fatal("second sniff must be clean (dirty read-clears)")
	}
}

func TestSessionCloseTerminates(t *testing.T) {
	bin := fixtureAgent(t)
	s, err := Start([]string{bin}, t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitFrame(t, s, "ready>")

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if s.IsAlive() {
		t.Fatal("session still alive after Close")
	}
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done not closed after Close")
	}
	if err := s.Write([]byte("x\n")); err == nil {
		t.Fatal("Write after Close must error")
	}

	// Close is idempotent.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}
