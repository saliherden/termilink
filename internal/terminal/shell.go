package terminal

import (
	"context"
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
	shellMarkerPrefix = "tlmk"
	shellSentinel     = "TLMP> "
	shellOpenTimeout  = 8 * time.Second
	shellEOLWait      = 2 * time.Second
)

var shellSeq uint64

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
	started time.Time
	pid     int
	zdir    string

	mu      sync.Mutex
	closed  bool
	done    chan struct{}
	curStop chan struct{}
	stopped atomic.Bool
}

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

func nextShellMarker() string {
	seq := atomic.AddUint64(&shellSeq, 1)
	return shellMarkerPrefix + "_" + strconv.Itoa(os.Getpid()) + "_" + strconv.FormatUint(seq, 10)
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
		buf:     newRingBuffer(DefaultRingBuffer),
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

	if err := s.waitReady(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Shell) waitReady() error {
	marker := nextShellMarker()
	init := "stty -echo; unsetopt zle; unsetopt flowcontrol; setopt no_beep; PROMPT='" + shellSentinel + "'; RPROMPT=''; echo REQ_" + marker + "\n"
	if err := s.writeString(init); err != nil {
		return err
	}
	deadline := time.Now().Add(shellOpenTimeout)
	for time.Now().Before(deadline) {
		if !s.IsAlive() {
			return fmt.Errorf("shell exited during startup")
		}
		if bytesContains(s.Output(), []byte("REQ_"+marker)) {
			s.buf.Reset()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("shell did not become ready within %s", shellOpenTimeout)
}

func (s *Shell) pump() {
	buf := make([]byte, 8192)
	for {
		n, err := s.master.Read(buf)
		if n > 0 {
			_, _ = s.buf.Write(buf[:n])
		}
		if err != nil {
			return
		}
	}
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
	marker := nextShellMarker()
	startTok := "S_" + marker + "#"
	stopTok := "E_" + marker + "#"
	frame := fmt.Sprintf("printf '\\n%s'; %s; printf 'TLM_PWD:%%s\n' \"$PWD\"; printf '%s'\n", startTok, command, stopTok)
	if err := s.writeString(frame); err != nil {
		return nil, err
	}
	startLen := len(s.Output())

	s.mu.Lock()
	s.curStop = make(chan struct{})
	stopCh := s.curStop
	s.mu.Unlock()
	s.stopped.Store(false)
	defer func() {
		s.stopped.Store(false)
		s.mu.Lock()
		s.curStop = nil
		s.mu.Unlock()
	}()

	cmdFinished := func(data []byte) []byte {
		ei := indexOf(data, []byte(stopTok))
		if ei < 0 {
			return nil
		}
		si := indexOf(data, []byte(startTok))
		if si < 0 || si > ei {
			si = 0
		}
		return data[si+len(startTok) : ei]
	}

	partial := func() []byte {
		out := s.Output()
		if startLen < len(out) {
			out = out[startLen:]
		}
		return lastBytes(out, interruptTailBytes)
	}

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.interruptNow()
			time.Sleep(shellEOLWait)
			if out := cmdFinished(s.Output()); out != nil {
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
			out := cmdFinished(s.Output())
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

// ParsePWD extracts the trailing "TLM_PWD:..." line from command output.
func ParsePWD(out []byte) string {
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "TLM_PWD:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "TLM_PWD:"))
		}
	}
	return ""
}

// CwdOfShell reports the current working directory of the interactive shell.
func (s *Shell) Cwd(ctx context.Context) (string, error) {
	out, err := s.ExecCommand(ctx, "printf .")
	if err != nil {
		return "", err
	}
	return ParsePWD(out), nil
}
