package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Resolve decides which coding-agent CLI the bot launches, and a wrong answer
// either starts the wrong tool or fails before a PTY is opened. It had no test,
// so the auto-detect order was only ever exercised by hand.

func fakeBin(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// pathWith builds a PATH containing exactly the named candidates and nothing
// else, so the result depends on the detect order rather than the real machine.
func pathWith(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		fakeBin(t, dir, n)
	}
	t.Setenv("PATH", dir)
	return dir
}

func TestResolveAutoDetectFollowsCandidateOrder(t *testing.T) {
	// candidates is opencode, claude, codex, gemini. With opencode and claude
	// both present the earlier one has to win.
	dir := pathWith(t, "opencode", "claude")
	got, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "opencode"); got != want {
		t.Fatalf("Resolve(\"\") = %q, want %q", got, want)
	}
}

func TestResolveAutoDetectSkipsMissingCandidates(t *testing.T) {
	// Only gemini and codex are installed: codex comes first in the list.
	dir := pathWith(t, "gemini", "codex")
	got, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "codex"); got != want {
		t.Fatalf("Resolve(\"\") = %q, want %q", got, want)
	}
}

func TestResolveAutoDetectUsesLastCandidate(t *testing.T) {
	dir := pathWith(t, "gemini")
	got, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "gemini"); got != want {
		t.Fatalf("Resolve(\"\") = %q, want %q", got, want)
	}
}

func TestResolveAutoDetectFindsNothing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := Resolve("")
	if !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("Resolve(\"\") error = %v, want ErrAgentNotFound", err)
	}
}

// An unrelated executable on PATH must not be mistaken for an agent: the
// candidates list is the whole allowlist.
func TestResolveAutoDetectIgnoresUnrelatedBinaries(t *testing.T) {
	pathWith(t, "sh", "ls", "git")
	if _, err := Resolve(""); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("an unrelated binary was treated as an agent: %v", err)
	}
}

func TestResolveExplicitName(t *testing.T) {
	dir := pathWith(t, "opencode")
	got, err := Resolve("opencode")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "opencode"); got != want {
		t.Fatalf("Resolve = %q, want %q", got, want)
	}
}

func TestResolveExplicitNameNotFound(t *testing.T) {
	pathWith(t)
	_, err := Resolve("claude")
	if err == nil {
		t.Fatal("expected an error for a missing explicit command")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error does not say the command was not found: %v", err)
	}
	if errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("a missing explicit command reported ErrAgentNotFound: %v", err)
	}
}

func TestResolveAbsolutePathSkipsLookup(t *testing.T) {
	// A configured path must work even when it is not on PATH, which is the
	// whole point of accepting an absolute path.
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	p := fakeBin(t, dir, "my-agent")

	got, err := Resolve(p)
	if err != nil {
		t.Fatal(err)
	}
	if got != p {
		t.Fatalf("Resolve = %q, want %q", got, p)
	}
}

func TestResolveRejectsNonExecutablePath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	p := filepath.Join(dir, "not-exec")
	if err := os.WriteFile(p, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(p); err == nil {
		t.Fatal("a non-executable file was accepted as an agent binary")
	}
}
