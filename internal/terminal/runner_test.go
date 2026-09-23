package terminal

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestExecuteBasic(t *testing.T) {
	r := NewRunner("/bin/sh", 5*time.Second, 1<<20)
	res, err := r.Execute(context.Background(), "", "printf 'hello'", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0", res.ExitCode)
	}
	if got := string(res.Output); got != "hello" {
		t.Fatalf("output = %q, want %q", got, "hello")
	}
}

func TestExecuteStderrMixed(t *testing.T) {
	r := NewRunner("/bin/sh", 5*time.Second, 1<<20)
	res, err := r.Execute(context.Background(), "", "echo out; echo err >&2; printf 'done'", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.TimedOut {
		t.Fatal("unexpected timeout")
	}
	for _, want := range []string{"out", "err", "done"} {
		if !strings.Contains(string(res.Output), want) {
			t.Fatalf("output missing %q: %q", want, res.Output)
		}
	}
}

func TestExecuteNonZeroExit(t *testing.T) {
	r := NewRunner("/bin/sh", 5*time.Second, 1<<20)
	res, err := r.Execute(context.Background(), "", "exit 3", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 {
		t.Fatalf("exit code = %d, want 3", res.ExitCode)
	}
}

func TestExecuteChunks(t *testing.T) {
	r := NewRunner("/bin/sh", 5*time.Second, 1<<20)
	var streams []StreamType
	res, err := r.Execute(context.Background(), "", "printf 'a'; printf 'b' >&2", func(c Chunk) {
		streams = append(streams, c.Stream)
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0", res.ExitCode)
	}
	if len(streams) == 0 {
		t.Fatal("expected at least one chunk")
	}
}

func TestExecuteTimeout(t *testing.T) {
	r := NewRunner("/bin/sh", 200*time.Millisecond, 1<<20)
	res, err := r.Execute(context.Background(), "", "sleep 5", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut {
		t.Fatal("expected timeout flag")
	}
}

func TestExecuteCWD(t *testing.T) {
	r := NewRunner("/bin/sh", 5*time.Second, 1<<20)
	dir := t.TempDir()
	res, err := r.Execute(context.Background(), dir, "pwd", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(res.Output)); got != dir {
		t.Fatalf("cwd = %q, want %q", got, dir)
	}
}

func TestTruncateOutput(t *testing.T) {
	data := []byte(strings.Repeat("x", 100))
	got := TruncateOutput(data, 40)
	if len(got) > 40 {
		t.Fatalf("truncated length = %d, want <= 40", len(got))
	}
	if !strings.Contains(string(got), "truncated") {
		t.Fatalf("truncated output missing marker: %q", got)
	}
	if got := TruncateOutput(data, 0); string(got) != string(data) {
		t.Fatal("zero cap must pass through data")
	}
	if TruncateOutput(nil, 10) != nil {
		t.Fatal("nil must stay nil")
	}
}