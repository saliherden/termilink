package telegram

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/saliherden/termilink/internal/config"
	"github.com/saliherden/termilink/internal/session"
	"github.com/saliherden/termilink/internal/terminal"
)

func TestGetShellAndExec(t *testing.T) {
	h := NewHandler(Options{
		Runner: terminal.NewRunner("/bin/zsh", 10*time.Second, 1<<20),
	})
	st := &session.State{ID: "chat-1"}
	shell, err := h.getShell(st)
	if err != nil {
		t.Fatalf("getShell: %v", err)
	}
	if !shell.IsAlive() {
		t.Fatal("shell not alive")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := shell.ExecCommand(ctx, "echo handler-pty-ok")
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if !bytes.Contains(out, []byte("handler-pty-ok")) {
		t.Fatalf("output missing echo: %q", out)
	}

	before := shell.PID()
	h.closeShellFor(st.ID)
	if shell.IsAlive() {
		t.Fatal("shell still alive after close")
	}

	shell2, err := h.getShell(st)
	if err != nil {
		t.Fatalf("getShell after close: %v", err)
	}
	if shell2.PID() == before {
		t.Fatal("expected a fresh shell after close")
	}

	h.Close()
}

func TestProjectCommandThroughShell(t *testing.T) {
	h := NewHandler(Options{
		Runner: terminal.NewRunner("/bin/zsh", 10*time.Second, 1<<20),
		Projects: map[string]config.ProjectConfig{
			"app": {
				Path:     t.TempDir(),
				Commands: map[string]string{"greet": "echo hello-from-app"},
			},
		},
	})
	h.closeShellFor("chat-2")
	st := &session.State{ID: "chat-2", Project: "app", Cwd: h.projects["app"].Path}
	command, isCd := h.resolve(st, "greet")
	if isCd {
		t.Fatal("greet should not be a cd")
	}
	shell, err := h.getShell(st)
	if err != nil {
		t.Fatalf("getShell: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := shell.ExecCommand(ctx, command)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if !bytes.Contains(out, []byte("hello-from-app")) {
		t.Fatalf("shortcut did not expand: %q", out)
	}
	h.Close()
}

func TestShellTairRemainsConsistent(t *testing.T) {
	h := NewHandler(Options{
		Runner: terminal.NewRunner("/bin/zsh", 10*time.Second, 1<<20),
	})
	defer h.Close()
	st := &session.State{ID: "chat-3"}
	shell, err := h.getShell(st)
	if err != nil {
		t.Fatalf("getShell: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := shell.ExecCommand(ctx, "echo first"); err != nil {
		t.Fatalf("first exec: %v", err)
	}
	tail := shell.Tail(256)
	if !bytes.Contains([]byte(tail), []byte("first")) {
		t.Fatalf("tail missing early output: %q", tail)
	}
}
