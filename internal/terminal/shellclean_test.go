package terminal

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestCleanShellOutput(t *testing.T) {
	in := "hello\n" +
		"TLM_PWD:/some/dir\n" +
		"S_tlmk_1_2#\n" +
		"real output line\n" +
		"REQ_tlmk_1_2\n" +
		"E_tlmk_1_2#\n" +
		"TLMP> \n" +
		"tail line\n"
	got := string(CleanShellOutput([]byte(in)))
	for _, keep := range []string{"hello", "real output line", "tail line"} {
		if !strings.Contains(got, keep) {
			t.Errorf("CleanShellOutput dropped %q; got %q", keep, got)
		}
	}
	for _, drop := range []string{"TLM_PWD:", "S_tlmk_", "REQ_tlmk_", "E_tlmk_"} {
		if strings.Contains(got, drop) {
			t.Errorf("CleanShellOutput kept %q; got %q", drop, got)
		}
	}
	if strings.Contains(got, "TLMP>") {
		t.Errorf("CleanShellOutput kept sentinel prompt; got %q", got)
	}
}

func TestCleanShellOutputEmpty(t *testing.T) {
	if got := CleanShellOutput(nil); got != nil {
		t.Fatalf("nil input should return nil, got %q", got)
	}
	if got := string(CleanShellOutput([]byte(""))); got != "" {
		t.Fatalf("empty input should stay empty, got %q", got)
	}
}

func TestLastBytes(t *testing.T) {
	data := []byte("0123456789")
	if got := string(lastBytes(data, 4)); got != "6789" {
		t.Fatalf("lastBytes(4) = %q", got)
	}
	if got := string(lastBytes(data, 20)); got != "0123456789" {
		t.Fatalf("lastBytes(20) = %q", got)
	}
}

func TestRingBufferReset(t *testing.T) {
	b := newRingBuffer(64)
	_, _ = b.Write([]byte("junk"))
	b.Reset()
	if len(b.Bytes()) != 0 {
		t.Fatalf("buffer not empty after reset: %d", len(b.Bytes()))
	}
}

func TestStopReturnsBoundedOutput(t *testing.T) {
	r := testRunner()
	s, err := r.OpenShell("", nil)
	if err != nil {
		t.Fatalf("open shell: %v", err)
	}
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	if out, err := s.ExecCommand(ctx, "echo AAA"); err != nil {
		t.Fatalf("prepare: %v", err)
	} else if !strings.Contains(string(out), "AAA") {
		t.Fatalf("prepare output = %q", out)
	}

	errCh := make(chan struct{})
	var out []byte
	var ee error
	go func() {
		out, ee = s.ExecCommand(ctx, "sleep 30")
		close(errCh)
	}()

	time.Sleep(300 * time.Millisecond)
	_ = s.Stop()

	<-errCh
	if len(out) > 4096 {
		t.Fatalf("stop returned %d bytes, want <=4096", len(out))
	}
	if ee == nil {
		t.Fatal("expected interrupted error")
	}
	if strings.Contains(string(out), "AAA") {
		t.Fatalf("interrupted output leaked previous command; got %q", out)
	}
}
