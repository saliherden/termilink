package terminal

import (
	"bytes"
	"context"
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
