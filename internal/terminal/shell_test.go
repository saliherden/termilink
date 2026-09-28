package terminal

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testRunner() *Runner {
	return NewRunner("/bin/zsh", 10*time.Second, 1<<20)
}

func TestShellExecEcho(t *testing.T) {
	r := testRunner()
	s, err := r.OpenShell("", nil)
	if err != nil {
		t.Fatalf("open shell: %v", err)
	}
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := s.ExecCommand(ctx, "echo pty-hello-123")
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if !bytes.Contains(out, []byte("pty-hello-123")) {
		t.Fatalf("output missing echo result: %q", out)
	}
}

func TestShellStopInterrupts(t *testing.T) {
	r := testRunner()
	s, err := r.OpenShell("", nil)
	if err != nil {
		t.Fatalf("open shell: %v", err)
	}
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	var out []byte
	go func() {
		var e error
		out, e = s.ExecCommand(ctx, "sleep 30")
		_ = out
		errCh <- e
	}()

	time.Sleep(200 * time.Millisecond)
	if err := s.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}

	select {
	case e := <-errCh:
		if e == nil {
			t.Fatal("expected interrupted error")
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("command was not interrupted in time")
	}

	again, err := s.ExecCommand(ctx, "echo after")
	if err != nil {
		t.Fatalf("shell unusable after stop: %v", err)
	}
	if !bytes.Contains(again, []byte("after")) {
		t.Fatalf("unexpected output: %q", again)
	}
}

func TestShellCwdInDir(t *testing.T) {
	r := testRunner()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := r.OpenShell(dir, nil)
	if err != nil {
		t.Fatalf("open shell: %v", err)
	}
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := s.ExecCommand(ctx, "pwd")
	if err != nil {
		t.Fatalf("pwd: %v", err)
	}
	pwd := ParsePWD(out)
	if pwd != dir {
		t.Fatalf("cwd = %q, want %q (out: %q)", pwd, dir, out)
	}
}

func TestShellPersistenceAcrossCommands(t *testing.T) {
	r := testRunner()
	s, err := r.OpenShell("", nil)
	if err != nil {
		t.Fatalf("open shell: %v", err)
	}
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.ExecCommand(ctx, "export FOO=bar"); err != nil {
		t.Fatalf("set env: %v", err)
	}
	out, err := s.ExecCommand(ctx, "echo $FOO")
	if err != nil {
		t.Fatalf("read env: %v", err)
	}
	if !strings.Contains(string(out), "bar") {
		t.Fatalf("env not preserved: %q", out)
	}
}

// TestOpenShellTurnsEchoOff guards the invariant every command frame depends
// on. If echo is still on, the PTY sends back the line we just wrote, and since
// that echo contains the frame markers verbatim, ExecCommand reports the
// command as finished before it has run and hands the caller its own command
// line instead of its output.
//
// One occurrence of the probe means the command ran and nothing echoed it;
// two means the echo is on.
//
// The wait is for the prompt sentinel that follows the probe, not for the
// probe's first appearance: the echo arrives first, so stopping at the first
// sighting would read a single occurrence and pass while echo is on.
func TestOpenShellTurnsEchoOff(t *testing.T) {
	const attempts = 5
	probe := "probe-" + nextShellMarker()

	for i := 1; i <= attempts; i++ {
		func() {
			r := testRunner()
			s, err := r.OpenShell("", nil)
			if err != nil {
				t.Fatalf("attempt %d: open shell: %v", i, err)
			}
			defer s.Close()

			if err := s.Write([]byte("echo " + probe + "\n")); err != nil {
				t.Fatalf("attempt %d: write probe: %v", i, err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				// Sentinel after the probe means the command has finished.
				if at := lastIndexOf(s.Output(), []byte(probe)); at >= 0 &&
					lastIndexOf(s.Output(), []byte(shellSentinel)) > at {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if n := countOccurrences(string(s.Output()), probe); n != 1 {
				t.Fatalf("attempt %d: probe appears %d times, want 1 (echo is on)\n%s",
					i, n, describeTerminalState(s))
			}
		}()
	}
}

// describeTerminalState renders what the shell thinks its terminal is doing,
// for the failure message. The terminal mode is read by typing a command
// directly rather than through ExecCommand, which is the thing under suspicion.
func describeTerminalState(s *Shell) string {
	_ = s.Write([]byte("stty -a; echo done-$?; zsh -c 'echo ZSH=$ZSH_VERSION'\n"))
	time.Sleep(500 * time.Millisecond)
	return fmt.Sprintf("buffer: %q", string(s.Output()))
}

func countOccurrences(hay, needle string) int {
	c := 0
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			c++
		}
	}
	return c
}

// TestLastIndexOf pins the helper the frame slicing depends on, including the
// overlap case where a naive backwards scan that steps back one byte at a time
// would find the wrong offset.
func TestLastIndexOf(t *testing.T) {
	hay := []byte("S_mark# output S_mark#")
	if got := lastIndexOf(hay, []byte("S_mark#")); got != 15 {
		t.Fatalf("last offset = %d, want 15", got)
	}
	if got := lastIndexOf(hay, []byte("nope")); got != -1 {
		t.Fatalf("missing needle = %d, want -1", got)
	}
	if got := lastIndexOf([]byte("aaaa"), []byte("aa")); got != 2 {
		t.Fatalf("overlap offset = %d, want 2", got)
	}
}

// TestSliceFrameSurvivesShellEcho is the regression guard for output that
// vanished while still reporting success. When the shell echoes the frame it
// was sent, the echo contains both markers verbatim, so scanning for the first
// match returned the echoed command line and threw the real output away. The
// buffer here is exactly that shape: the echoed frame, then what the shell
// actually printed.
func TestSliceFrameSurvivesShellEcho(t *testing.T) {
	marker := "tlmk_1_7"
	startTok := "S_" + marker + "#"
	stopTok := "E_" + marker + "#"
	cmd := `echo "[cwd=$PWD][project=$TERMILINK_PROJECT]"`
	frame := fmt.Sprintf("printf '\\n%s'; %s; printf 'TLM_PWD:%%s\\n' \"$PWD\"; printf '%s'\\n",
		startTok, cmd, stopTok)

	echoed := frame + startTok + "\n[cwd=/home/x][project=]\nTLM_PWD:/home/x\n" + stopTok

	got := string(sliceFrame([]byte(echoed), startTok, stopTok))
	if !strings.Contains(got, "[project=]") {
		t.Fatalf("real output lost to the shell echo: %q", got)
	}
	if strings.Contains(got, "printf 'TLM_PWD:") {
		t.Fatalf("returned the echoed frame instead of the output: %q", got)
	}
}

// TestSliceFrameWaitsForCloseMarker keeps the nil-until-done contract: without
// the closing marker there is no result to report yet.
func TestSliceFrameWaitsForCloseMarker(t *testing.T) {
	marker := "tlmk_1_8"
	startTok := "S_" + marker + "#"
	stopTok := "E_" + marker + "#"

	partial := []byte("S_" + marker + "#\npartial output\n")
	if got := sliceFrame(partial, startTok, stopTok); got != nil {
		t.Fatalf("returned a result before the close marker: %q", got)
	}
}

func TestRingBufferTail(t *testing.T) {
	b := newRingBuffer(32)
	for i := 0; i < 5; i++ {
		_, _ = b.Write([]byte("0123456789"))
	}
	if got := len(b.Bytes()); got != 32 {
		t.Fatalf("ring len = %d, want 32", got)
	}
	full := b.Bytes()
	if got := b.Tail(4); got != string(full[len(full)-4:]) {
		t.Fatalf("tail mismatch: %q", got)
	}
}
