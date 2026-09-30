package terminal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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

func TestShellCwdIgnoresForgedMarker(t *testing.T) {
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
	// The command prints a forged marker before anything else. The frame emits
	// the shell's own cwd last, so that is the one the shell is reporting.
	out, err := s.ExecCommand(ctx, `(printf 'TLM_PWD:/etc\n'; pwd)`)
	if err != nil {
		t.Fatalf("pwd: %v", err)
	}
	if pwd := ParsePWD(out); pwd != dir {
		t.Fatalf("forged marker won: cwd = %q, want %q (out: %q)", pwd, dir, out)
	}
}

// A command that guesses the frame marker could hide part of its own output
// from the report. sliceFrame takes the last start token before the end token,
// so a forged start token planted in the command's own output wins over the
// real one and silently drops everything printed before it. The audit log
// records the command and its status but not its output, so the hidden lines
// are not recoverable afterwards.
//
// This pins the property rather than the attack: the guess below is one
// attempt, because pinning the counter it used to read is exactly the coupling
// being removed. TestShellMarkerIsUnpredictable is what fails if the marker
// goes back to being derived from the pid and a counter.
func TestCommandCannotHideItsOwnOutput(t *testing.T) {
	r := testRunner()
	s, err := r.OpenShell("", nil)
	if err != nil {
		t.Fatalf("open shell: %v", err)
	}
	defer s.Close()

	// Guess the way an attacker would. The marker used to be the pid plus a
	// counter, both readable, so this is the shape that used to work.
	forged := shellMarkerPrefix + "_" + strconv.Itoa(os.Getpid()) + "_1"
	command := fmt.Sprintf("printf 'FIRST-LINE\\nS_%s#\\nE_%s#\\nAFTER-LINE\\n'", forged, forged)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := s.ExecCommand(ctx, command)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	// The forged end token cannot truncate anything: the real one is written
	// last, and the frame takes the last match.
	if !bytes.Contains(out, []byte("AFTER-LINE")) {
		t.Errorf("output truncated at the end: %q", out)
	}
	if !bytes.Contains(out, []byte("FIRST-LINE")) {
		t.Errorf("output truncated at the start by a guessed frame token: %q", out)
	}
}

// A fixed marker would be as forgeable as a predictable one, so the format is
// pinned: the unpredictable part has to be hex, and long enough that guessing
// it is not an option. crypto/rand.Text is deliberately not used here — it
// discards its own error, so a broken entropy source would hand back a string
// of zeroes and the marker would become predictable while looking random.
func TestShellMarkerIsUnpredictable(t *testing.T) {
	shape := regexp.MustCompile(`^` + shellMarkerPrefix + `_[0-9a-f]{32}$`)
	seen := make(map[string]bool)
	for i := 0; i < 32; i++ {
		marker, err := nextShellMarker()
		if err != nil {
			t.Fatalf("marker: %v", err)
		}
		if !shape.MatchString(marker) {
			t.Fatalf("marker %q is not %d hex characters after the prefix", marker, 32)
		}
		if seen[marker] {
			t.Fatalf("marker %q was returned twice in %d draws", marker, i+1)
		}
		seen[marker] = true
	}
}

func TestParsePWD(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want string
	}{
		{"plain", "out\nTLM_PWD:/tmp\n", "/tmp"},
		{"no marker", "out\n", ""},
		{"empty", "", ""},
		{"trailing spaces", "out\nTLM_PWD:  /tmp  \n", "/tmp"},
		{"forged before the real cwd", "TLM_PWD:/etc\nout\nTLM_PWD:/tmp\n", "/tmp"},
		{"several forged lines", "TLM_PWD:/etc\nTLM_PWD:/var\nout\nTLM_PWD:/tmp\n", "/tmp"},
		{"forged line after the command's output", "out\nTLM_PWD:/etc\nout\nTLM_PWD:/tmp\n", "/tmp"},
		{"forged cwd with carriage returns", "TLM_PWD:/etc\r\nout\r\nTLM_PWD:/tmp\r\n", "/tmp"},
		{"only a forged line", "TLM_PWD:/etc\nout\n", "/etc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParsePWD([]byte(tc.out)); got != tc.want {
				t.Fatalf("ParsePWD(%q) = %q, want %q", tc.out, got, tc.want)
			}
		})
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
	marker, err := nextShellMarker()
	if err != nil {
		t.Fatalf("marker: %v", err)
	}
	probe := "probe-" + marker

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
			if n := countOccurrences(s.Output(), probe); n != 1 {
				t.Fatalf("attempt %d: probe appears %d times, want 1 (echo is on)\n%s",
					i, n, s.diagnostics())
			}
		}()
	}
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

// TestParseExitCode covers the parser on its own, including the shapes it has
// to survive: \r from the PTY, a missing line, a line that is not a number, and
// a command that prints a TLM_RC line of its own to lie about the status.
func TestParseExitCode(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want int
	}{
		{"zero", "out\nTLM_RC:0\n", 0},
		{"nonzero", "out\nTLM_RC:1\n", 1},
		{"signal", "out\nTLM_RC:137\n", 137},
		{"carriage return", "out\r\nTLM_RC:3\r\n", 3},
		{"padded", "out\nTLM_RC:  42  \n", 42},
		{"absent", "out only\n", -1},
		{"empty", "", -1},
		{"not a number", "out\nTLM_RC:weird\n", -1},
		// The frame's own marker comes last, so a command that prints one
		// first is ignored.
		{"forged before the real status", "TLM_RC:0\nout\nTLM_RC:1\n", 1},
		{"several forged lines", "TLM_RC:0\nTLM_RC:0\nout\nTLM_RC:9\n", 9},
		{"forged line is not a number", "TLM_RC:weird\nout\nTLM_RC:1\n", 1},
		{"forged line inside real output", "before\nTLM_RC:0\nafter\nTLM_RC:255\n", 255},
		{"forged status with carriage returns", "TLM_RC:0\r\nout\r\nTLM_RC:1\r\n", 1},
		{"only a forged line, no real one", "TLM_RC:0\nout\n", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseExitCode([]byte(tc.out)); got != tc.want {
				t.Fatalf("ParseExitCode(%q) = %d, want %d", tc.out, got, tc.want)
			}
		})
	}
}

// TestShellReportsRealExitCode is the terminal-side half: the status has to
// survive the round trip through a real interactive zsh, not just the parser.
func TestShellReportsRealExitCode(t *testing.T) {
	r := testRunner()
	s, err := r.OpenShell("", nil)
	if err != nil {
		t.Fatalf("open shell: %v", err)
	}
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if out, err := s.ExecCommand(ctx, "false"); err != nil {
		t.Fatalf("exec false: %v", err)
	} else if got := ParseExitCode(out); got != 1 {
		t.Fatalf("false reported %d, want 1; raw: %q", got, out)
	}

	if out, err := s.ExecCommand(ctx, "true"); err != nil {
		t.Fatalf("exec true: %v", err)
	} else if got := ParseExitCode(out); got != 0 {
		t.Fatalf("true reported %d, want 0; raw: %q", got, out)
	}

	// A bare `exit` would end the interactive shell itself, so the status comes
	// from a subshell instead.
	if out, err := s.ExecCommand(ctx, "(exit 7)"); err != nil {
		t.Fatalf("exec subshell: %v", err)
	} else if got := ParseExitCode(out); got != 7 {
		t.Fatalf("subshell reported %d, want 7; raw: %q", got, out)
	}

	// The command frame prints TLM_RC after the command's own output, so a
	// command that prints a TLM_RC line itself is trying to speak for the exit
	// status. This failed for real: `exit 1` with a forged 0 in front of it was
	// reported to the owner as a success. The status comes from a subshell
	// because a bare `exit` would take the interactive shell with it.
	if out, err := s.ExecCommand(ctx, `(printf 'TLM_RC:0\n'; exit 1)`); err != nil {
		t.Fatalf("exec forger: %v", err)
	} else if got := ParseExitCode(out); got != 1 {
		t.Fatalf("forged exit status was believed: got %d, want 1; raw: %q", got, out)
	}
}

// The marker is plumbing, not output: the owner must never see it.
func TestCleanShellOutputStripsExitCodeMarker(t *testing.T) {
	raw := []byte("hello\nTLM_RC:1\nTLM_PWD:/tmp\n")
	clean := string(CleanShellOutput(raw))
	if strings.Contains(clean, "TLM_RC:") || strings.Contains(clean, "TLM_PWD:") {
		t.Fatalf("markers survived cleaning: %q", clean)
	}
	if !strings.Contains(clean, "hello") {
		t.Fatalf("cleaning ate real output: %q", clean)
	}
}

// TestInterruptedCommandDoesNotBorrowEarlierOutput is the regression for the
// per-command capture.
//
// partial() used to return the last few kilobytes of the session buffer, which
// spans every command the shell has ever run. Interrupting a command that had
// not produced output yet therefore handed the owner the *previous* command's
// output, and with it the previous command's TLM_RC line — which ParseExitCode
// then read as the interrupted command's status. A timed-out or stopped
// command was reported with the exit code of the command before it.
func TestInterruptedCommandDoesNotBorrowEarlierOutput(t *testing.T) {
	r := testRunner()
	s, err := r.OpenShell("", nil)
	if err != nil {
		t.Fatalf("open shell: %v", err)
	}
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := s.ExecCommand(ctx, `echo EARLIER-MARKER-UNIQUE; (exit 7)`); err != nil {
		t.Fatalf("first command: %v", err)
	}

	short, shortCancel := context.WithTimeout(ctx, 700*time.Millisecond)
	defer shortCancel()
	out, err := s.ExecCommand(short, `sleep 5`)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context deadline exceeded", err)
	}
	if strings.Contains(string(out), "EARLIER-MARKER-UNIQUE") {
		t.Fatalf("interrupted command borrowed the previous command's output: %q", out)
	}
	// The command never reported a status of its own, so there is nothing to
	// report. -1 is "no status", which is what the owner has to be shown rather
	// than a stale 0 or 7.
	if code := ParseExitCode(out); code != -1 {
		t.Fatalf("interrupted command reported exit code %d, want -1 (no status of its own)", code)
	}
}

// TestMaxOutputBytesReachesTheCapture covers terminal.max_output_bytes, which
// the Runner carried and OpenShell ignored — it built the buffer from the
// hardcoded DefaultRingBuffer. Raising the limit in the config did nothing, and
// a command that outgrew 512 KiB lost the front of its own output.
func TestMaxOutputBytesReachesTheCapture(t *testing.T) {
	r := NewRunner("/bin/zsh", 30*time.Second, 4<<20)
	s, err := r.OpenShell("", nil)
	if err != nil {
		t.Fatalf("open shell: %v", err)
	}
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// ~1.6 MiB: over the 512 KiB default, under the 4 MiB configured here.
	out, err := s.ExecCommand(ctx,
		`echo FIRST-LINE-OF-THE-COMMAND; for i in $(seq 1 40000); do echo "line-$i-cccccccccccccccccccccccc"; done; (exit 0)`)
	if err != nil {
		t.Fatalf("big command: %v", err)
	}
	if len(out) < 1<<20 {
		t.Fatalf("captured %d bytes, want at least 1 MiB: the configured limit is not reaching the capture", len(out))
	}
	if !strings.Contains(string(out), "FIRST-LINE-OF-THE-COMMAND") {
		t.Fatalf("the start of the output was evicted at a 4 MiB limit")
	}
	if code := ParseExitCode(out); code != 0 {
		t.Fatalf("ParseExitCode = %d, want 0", code)
	}
}

// A small configured limit must not break the frame. It costs output, not
// correctness: the stop marker is written last and always survives.
func TestSmallMaxOutputStillReportsStatus(t *testing.T) {
	r := NewRunner("/bin/zsh", 30*time.Second, 1)
	s, err := r.OpenShell("", nil)
	if err != nil {
		t.Fatalf("open shell: %v", err)
	}
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := s.ExecCommand(ctx, `for i in $(seq 1 5000); do echo "noise-$i"; done; (exit 3)`)
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	if code := ParseExitCode(out); code != 3 {
		t.Fatalf("ParseExitCode = %d, want 3: a tiny capture must still report the real status", code)
	}
}

func TestCaptureSizeFloor(t *testing.T) {
	cases := []struct {
		configured, want int
	}{
		{0, DefaultRingBuffer},
		{-1, DefaultRingBuffer},
		{1, MinRingBuffer},
		{1024, MinRingBuffer},
		{MinRingBuffer, MinRingBuffer},
		{4 << 20, 4 << 20},
	}
	for _, tc := range cases {
		if got := captureSize(tc.configured); got != tc.want {
			t.Fatalf("captureSize(%d) = %d, want %d", tc.configured, got, tc.want)
		}
	}
}
