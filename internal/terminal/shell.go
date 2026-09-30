package terminal

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/creack/pty"
)

const (
	DefaultRingBuffer = 512 * 1024
	// MinRingBuffer is the floor for any capture buffer. A command's output has
	// to fit its own completion markers and enough context to be worth reading;
	// below this the frame degenerates to a few hundred bytes. A small
	// configured value is not unsafe, only useless — see ExecCommand.
	MinRingBuffer     = 64 * 1024
	shellMarkerPrefix = "tlmk"
	shellSentinel     = "TLMP> "
	shellOpenTimeout  = 8 * time.Second
	shellEOLWait      = 2 * time.Second
	// setupAttempts bounds how many times the shell is configured before
	// giving up on getting the echo off.
	setupAttempts = 3
)

type ringBuffer struct {
	mu   sync.Mutex
	data []byte
	max  int
}

func newRingBuffer(max int) *ringBuffer {
	if max <= 0 {
		max = DefaultRingBuffer
	}
	return &ringBuffer{max: max}
}

// captureSize resolves a configured output limit to the size a capture buffer
// actually gets. The floor is a usefulness policy, not a safety one: a command
// whose output outgrows its buffer still finishes and still reports its own
// status, because the stop marker is written last and always survives. Below
// the floor the result is technically correct and practically unreadable.
func captureSize(configured int) int {
	size := newRingBuffer(configured).max
	if size < MinRingBuffer {
		return MinRingBuffer
	}
	return size
}

// Contains reports whether the buffer holds needle. ExecCommand's poll asks
// this every 50ms, and Bytes would copy the whole buffer to answer it — which
// is a megabyte twenty times a second for a command that has produced a
// megabyte of output.
func (b *ringBuffer) Contains(needle []byte) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Contains(b.data, needle)
}

func (b *ringBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if len(b.data) >= b.max {
		drop := len(b.data) - b.max
		n := make([]byte, b.max)
		copy(n, b.data[drop:])
		b.data = n
	}
	return len(p), nil
}

func (b *ringBuffer) Reset() {
	b.mu.Lock()
	b.data = nil
	b.mu.Unlock()
}

func (b *ringBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]byte, len(b.data))
	copy(out, b.data)
	return out
}

func (b *ringBuffer) Tail(n int) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n <= 0 || n > len(b.data) {
		n = len(b.data)
	}
	return string(b.data[len(b.data)-n:])
}

// Shell is a persistent interactive PTY shell bound to a chat session.
type Shell struct {
	cmd     *exec.Cmd
	master  io.ReadWriteCloser
	buf     *ringBuffer
	maxOut  int
	started time.Time
	pid     int
	zdir    string

	mu      sync.Mutex
	closed  bool
	done    chan struct{}
	curStop chan struct{}
	// capture holds the output of the command currently running, and is nil
	// otherwise. It is a separate buffer from buf on purpose: buf spans the
	// whole session and rolls over, so a single command that outgrows it cannot
	// be told apart from the ones before it.
	capture *ringBuffer
	stopped atomic.Bool
}

// prepareZDotDir creates a throwaway ZDOTDIR with an empty .zshrc so the
// user's own shell configuration cannot interfere with command capture.
func prepareZDotDir() (string, error) {
	zdir, err := os.MkdirTemp("", "termilink-zdot-*")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(zdir, ".zshrc"), nil, 0o600); err != nil {
		_ = os.RemoveAll(zdir)
		return "", err
	}
	return zdir, nil
}

// nextShellMarker returns the per-command marker that delimits a command's
// output. It must be unpredictable: sliceFrame resolves the output by finding
// the last start token before the end token, so a command that can predict the
// marker can print a fake start token of its own and make everything printed
// before it disappear from the report. The audit log records the command and
// its status but not its output, so hidden lines leave no trace afterwards.
//
// The randomness comes from crypto/rand rather than a counter because a
// predictable marker is the vulnerability, not a hardening opportunity. The
// pid and a per-process counter were both readable by anything running inside
// the session, and the counter only ever went up, so a command could derive
// the marker of the command it was about to run.
func nextShellMarker() (string, error) {
	var b [16]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate shell marker: %w", err)
	}
	return shellMarkerPrefix + "_" + hex.EncodeToString(b[:]), nil
}

// OpenShell spawns an interactive shell attached to a pseudo-terminal. The
// shell runs with an isolated ZDOTDIR and a sentinel prompt so command output
// can be framed reliably.
func (r *Runner) OpenShell(dir string, env []string) (*Shell, error) {
	if r.shell == "" {
		return nil, fmt.Errorf("terminal: no shell configured")
	}
	zdir, err := prepareZDotDir()
	if err != nil {
		return nil, fmt.Errorf("prepare zdotdir: %w", err)
	}

	cmd := exec.Command(r.shell, "-i")
	cmd.Env = append(os.Environ(), env...)
	cmd.Env = append(cmd.Env,
		"TERM=dumb",
		"ZDOTDIR="+zdir,
		"PS1="+shellSentinel,
		"RPROMPT=",
		"PROMPT2="+shellSentinel,
		"PROMPT3="+shellSentinel,
		"PROMPT4="+shellSentinel,
	)
	if dir != "" {
		cmd.Dir = dir
	}

	master, err := pty.Start(cmd)
	if err != nil {
		_ = os.RemoveAll(zdir)
		return nil, fmt.Errorf("open pty: %w", err)
	}

	pid := 0
	if cmd.Process != nil {
		pid = cmd.Process.Pid
	}

	s := &Shell{
		cmd:     cmd,
		master:  master,
		buf:     newRingBuffer(captureSize(r.maxOutput)),
		maxOut:  r.maxOutput,
		started: time.Now(),
		pid:     pid,
		zdir:    zdir,
		done:    make(chan struct{}),
	}

	go s.pump()
	go func() {
		_ = cmd.Wait()
		s.markDone()
	}()

	if err := s.startShell(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// startShell configures the shell for framed command capture and confirms the
// configuration actually took effect.
//
// The confirmation is the point. Sending the setup line and assuming it worked
// is what caused the bug this guards: the terminal may keep echoing after the
// line has run, and the PTY's echo carries the very markers ExecCommand reads,
// so every command gets reported finished before it has run and its caller is
// handed its own command line as the output. Whether the echo has stopped
// depends on the terminal and on the shell's line editor, so it is measured
// here rather than inferred — and if it cannot be stopped, startup fails
// loudly instead of returning results that are quietly wrong.
func (s *Shell) startShell() error {
	deadline := time.Now().Add(shellOpenTimeout)
	for attempt := 1; attempt <= setupAttempts; attempt++ {
		marker, err := nextShellMarker()
		if err != nil {
			return err
		}
		// The marker is parked in a shell variable instead of being pasted
		// into the echo argument, so the literal text "REQ_<marker>" never
		// appears in the line being sent. Otherwise the PTY's echo of this
		// very line would satisfy waitReady on its own.
		setup := "stty -echo; unsetopt zle; unsetopt flowcontrol; setopt no_beep; PROMPT='" +
			shellSentinel + "'; RPROMPT=''; m='" + marker + "'; echo REQ_$m\n"
		if err := s.writeString(setup); err != nil {
			return err
		}
		if err := s.waitReady(marker, deadline); err != nil {
			return err
		}
		echoed, err := s.echoIsOn()
		if err != nil {
			return err
		}
		if !echoed {
			return nil
		}
		s.buf.Reset()
	}
	return fmt.Errorf("terminal still echoing after %d attempts to configure it%s", setupAttempts, s.diagnostics())
}

// waitReady blocks until the shell reports the marker from the setup line.
func (s *Shell) waitReady(marker string, deadline time.Time) error {
	for time.Now().Before(deadline) {
		if !s.IsAlive() {
			return fmt.Errorf("shell exited during startup%s", s.diagnostics())
		}
		if bytesContains(s.Output(), []byte("REQ_"+marker)) {
			s.buf.Reset()
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("shell did not become ready within %s%s", shellOpenTimeout, s.diagnostics())
}

// echoIsOn measures whether the terminal echoes what we write. A probe that
// comes back once was only run by the shell; coming back twice means the echo
// is on as well. The wait is for the prompt that follows the probe, not for
// the probe's first appearance, because the echo arrives first.
func (s *Shell) echoIsOn() (bool, error) {
	marker, err := nextShellMarker()
	if err != nil {
		return false, err
	}
	probe := "tlprobe" + marker
	if err := s.writeString("echo " + probe + "\n"); err != nil {
		return false, err
	}
	deadline := time.Now().Add(shellEOLWait)
	for time.Now().Before(deadline) {
		if !s.IsAlive() {
			return false, fmt.Errorf("shell exited while probing for echo%s", s.diagnostics())
		}
		out := s.Output()
		at := lastIndexOf(out, []byte(probe))
		if at >= 0 && lastIndexOf(out, []byte(shellSentinel)) > at {
			s.buf.Reset()
			return countOccurrences(out, probe) > 1, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false, fmt.Errorf("shell did not run a probe within %s%s", shellEOLWait, s.diagnostics())
}

// diagnostics renders what the shell thinks its terminal is doing, for error
// messages. It asks the shell directly rather than going through
// ExecCommand, which is the thing under suspicion.
func (s *Shell) diagnostics() string {
	_ = s.writeString("stty -a; echo stty_status=$?; echo ZSH=$ZSH_VERSION\n")
	time.Sleep(300 * time.Millisecond)
	return fmt.Sprintf("\nbuffer: %q", string(s.Output()))
}

func countOccurrences(hay []byte, needle string) int {
	c := 0
	for i := 0; i+len(needle) <= len(hay); i++ {
		if string(hay[i:i+len(needle)]) == needle {
			c++
		}
	}
	return c
}

func (s *Shell) pump() {
	buf := make([]byte, 8192)
	for {
		n, err := s.master.Read(buf)
		if n > 0 {
			_, _ = s.buf.Write(buf[:n])
			if c := s.captureBuffer(); c != nil {
				_, _ = c.Write(buf[:n])
			}
		}
		if err != nil {
			return
		}
	}
}

// captureBuffer returns the buffer collecting the running command's output, or
// nil when nothing is running. The nil check is the normal case: pump reads
// from the pty continuously, including between commands.
func (s *Shell) captureBuffer() *ringBuffer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.capture
}

func (s *Shell) markDone() {
	s.mu.Lock()
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	s.mu.Unlock()
}

func (s *Shell) IsAlive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

func (s *Shell) PID() int {
	return s.pid
}

func (s *Shell) Age() time.Duration {
	return time.Since(s.started)
}

func (s *Shell) Tail(n int) string {
	return s.buf.Tail(n)
}

func (s *Shell) Output() []byte {
	return s.buf.Bytes()
}

func bytesContains(hay, needle []byte) bool {
	return len(needle) > 0 && indexOf(hay, needle) >= 0
}

func indexOf(hay, needle []byte) int {
	if len(needle) == 0 {
		return 0
	}
	for i := 0; i+len(needle) <= len(hay); i++ {
		if string(hay[i:i+len(needle)]) == string(needle) {
			return i
		}
	}
	return -1
}

// lastIndexOf returns the offset of the final occurrence of needle, or -1.
func lastIndexOf(hay, needle []byte) int {
	if len(needle) == 0 {
		return len(hay)
	}
	for i := len(hay) - len(needle); i >= 0; i-- {
		if string(hay[i:i+len(needle)]) == string(needle) {
			return i
		}
	}
	return -1
}

// sliceFrame returns the output a command produced between its markers, or nil
// if the closing marker has not arrived yet.
//
// It matches the LAST occurrence of each marker rather than the first. The
// shell can echo the frame it was sent, and that echo repeats the marker text
// verbatim, so a first-occurrence search finds the echoed command line, returns
// it as if it were the result, and silently discards the real output — the
// command appears to have run, reports success, and prints nothing. Echo is not
// guaranteed to be off, so the marker trick has to survive a shell that talks
// back. The genuine markers are the ones printed after the echo, which is why
// the start search is bounded by the stop offset.
func sliceFrame(data []byte, startTok, stopTok string) []byte {
	ei := lastIndexOf(data, []byte(stopTok))
	if ei < 0 {
		return nil
	}
	si := lastIndexOf(data[:ei], []byte(startTok))
	if si < 0 {
		si = 0
	}
	return data[si+len(startTok) : ei]
}

// Write sends raw bytes to the shell's stdin.
func (s *Shell) Write(data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("shell is closed")
	}
	_, err := s.master.Write(data)
	return err
}

func (s *Shell) writeString(str string) error {
	return s.Write([]byte(str))
}

// Stop interrupts the currently running foreground job with SIGINT (Ctrl-C).
// If no command is actively tracked it still sends the interrupt, best effort.
func (s *Shell) Stop() error {
	s.stopped.Store(true)
	s.mu.Lock()
	ch := s.curStop
	s.mu.Unlock()
	if ch != nil {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
	_ = s.writeString("\x03")
	return nil
}

func (s *Shell) interruptNow() {
	_ = s.writeString("\x03")
}

// Close terminates the shell process and releases the pty.
func (s *Shell) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	if s.curStop != nil {
		select {
		case <-s.curStop:
		default:
			close(s.curStop)
		}
	}
	s.mu.Unlock()

	if s.cmd.Process != nil {
		_ = s.cmd.Process.Signal(os.Interrupt)
		time.AfterFunc(500*time.Millisecond, func() {
			if s.IsAlive() {
				_ = s.cmd.Process.Kill()
			}
		})
	}
	_ = s.master.Close()
	if s.zdir != "" {
		_ = os.RemoveAll(s.zdir)
		s.zdir = ""
	}
	return nil
}

// ExecCommand writes a command wrapped in unique markers to the interactive
// shell and waits until the end marker is observed, the stop signal fires or
// ctx is done. It returns the output between the markers.
func (s *Shell) ExecCommand(ctx context.Context, command string) ([]byte, error) {
	marker, err := nextShellMarker()
	if err != nil {
		return nil, err
	}
	startTok := "S_" + marker + "#"
	stopTok := "E_" + marker + "#"
	// TLM_RC captures the command's own status: $? is expanded while printf's
	// arguments are built, which is still the previous command's status, so the
	// printf itself cannot overwrite the value it is printing.
	frame := fmt.Sprintf("printf '\\n%s'; %s; printf 'TLM_RC:%%s\\n' \"$?\"; printf 'TLM_PWD:%%s\n' \"$PWD\"; printf '%s'\n", startTok, command, stopTok)

	// Install the per-command capture before the frame is written, so the
	// shell's echo of the start token is part of it. From here on this is the
	// only place the command's output is looked for.
	//
	// A buffer smaller than the command's output is not a failure here. The
	// stop token is written last, so it always survives, and sliceFrame then
	// falls back to the start of what is left — the tail of *this* command,
	// with no way to mistake it for an earlier one's output. That is why the
	// configured minimum is a matter of usefulness rather than correctness.
	capture := newRingBuffer(captureSize(s.maxOut))
	s.mu.Lock()
	s.capture = capture
	s.curStop = make(chan struct{})
	stopCh := s.curStop
	s.mu.Unlock()
	s.stopped.Store(false)
	defer func() {
		s.stopped.Store(false)
		s.mu.Lock()
		s.capture = nil
		s.curStop = nil
		s.mu.Unlock()
	}()

	if err := s.writeString(frame); err != nil {
		return nil, err
	}

	// cmdFinished returns the output the command actually produced, or nil while
	// the command is still running.
	cmdFinished := func() []byte {
		if !capture.Contains([]byte(stopTok)) {
			return nil
		}
		return sliceFrame(capture.Bytes(), startTok, stopTok)
	}

	partial := func() []byte {
		return lastBytes(capture.Bytes(), interruptTailBytes)
	}

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.interruptNow()
			time.Sleep(shellEOLWait)
			if out := cmdFinished(); out != nil {
				return out, ctx.Err()
			}
			return partial(), ctx.Err()
		case <-stopCh:
			time.Sleep(shellEOLWait)
			return partial(), ErrInterrupted
		case <-ticker.C:
			if s.stopped.Load() {
				return partial(), ErrInterrupted
			}
			out := cmdFinished()
			if out != nil {
				return out, nil
			}
			if !s.IsAlive() {
				return partial(), errors.New("shell exited while running command")
			}
		}
	}
}

const interruptTailBytes = 4096

func lastBytes(data []byte, n int) []byte {
	if n <= 0 || len(data) <= n {
		return data
	}
	return data[len(data)-n:]
}

var ErrInterrupted = errors.New("command interrupted")

// ParsePWD extracts the trailing "TLM_PWD:..." line from command output. It
// scans backwards and takes the last such line, because the frame emits the
// shell's own cwd after the command has run — anything the command printed came
// earlier in the byte stream, and a command must not be able to report a
// directory the shell is not actually in.
func ParsePWD(out []byte) string {
	lines := strings.Split(string(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimRight(lines[i], "\r")
		if strings.HasPrefix(line, "TLM_PWD:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "TLM_PWD:"))
		}
	}
	return ""
}

// ParseExitCode extracts the "TLM_RC:..." line the command frame emits, which
// carries the shell's own exit status for the command that just ran. It returns
// -1 when the line is absent — an output older than this frame format, a command
// that was interrupted before it could report, or a shell that never echoed the
// line back — which is the same "no status to report" value the one-shot runner
// uses when a process has no ProcessState.
//
// The frame prints TLM_RC *after* the command's own output, so the genuine line
// is the last one in the frame. Scanning forwards let a command forge its exit
// status: `(printf 'TLM_RC:0\n'; exit 1)` was reported to the owner as a
// success. Scanning backwards makes the frame's own marker authoritative, which
// is the only thing in the output this function has any business believing.
func ParseExitCode(out []byte) int {
	lines := strings.Split(string(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimRight(lines[i], "\r")
		if strings.HasPrefix(line, "TLM_RC:") {
			if n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "TLM_RC:"))); err == nil {
				return n
			}
			return -1
		}
	}
	return -1
}

// CwdOfShell reports the current working directory of the interactive shell.
func (s *Shell) Cwd(ctx context.Context) (string, error) {
	out, err := s.ExecCommand(ctx, "printf .")
	if err != nil {
		return "", err
	}
	return ParsePWD(out), nil
}
