package agent

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creack/pty"
)

// ErrNotRunning is returned when a write targets an already closed session.
var ErrNotRunning = errors.New("agent session is not running")

// Session is a live TUI agent process attached to a pseudo-terminal. Its raw
// output bytes are parsed into a Screen, so the visible frame can be relayed
// to a remote client, while user input is written back to the PTY.
type Session struct {
	cmd     *exec.Cmd
	master  io.ReadWriteCloser
	scrn    *Screen
	done    chan struct{}
	started time.Time
	pid     int

	mu     sync.Mutex
	closed bool

	onPkt func()
}

// Start spawns the agent binary (argv[0]) inside a PTY rooted at dir. The
// output is continuously parsed into an internal Screen. onPkt, when non-nil,
// is invoked (from the pump goroutine) after each chunk so the caller can track
// liveness/quiescence.
func Start(argv []string, dir string, env []string, onPkt func()) (*Session, error) {
	if len(argv) == 0 || argv[0] == "" {
		return nil, fmt.Errorf("agent: empty command")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Env = append(cmd.Env, "TERM=xterm-256color")

	master, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: screenRows, Cols: screenCols})
	if err != nil {
		return nil, fmt.Errorf("agent: open pty: %w", err)
	}

	s := &Session{
		cmd:     cmd,
		master:  master,
		scrn:    NewScreen(),
		done:    make(chan struct{}),
		started: time.Now(),
		onPkt:   onPkt,
	}
	if cmd.Process != nil {
		s.pid = cmd.Process.Pid
	}

	go s.pump()
	go func() {
		_ = cmd.Wait()
		s.markDone()
	}()
	return s, nil
}

func (s *Session) pump() {
	buf := make([]byte, 8192)
	for {
		n, err := s.master.Read(buf)
		if n > 0 {
			s.scrn.Feed(buf[:n])
			if s.onPkt != nil {
				s.onPkt()
			}
		}
		if err != nil {
			return
		}
	}
}

func (s *Session) markDone() {
	s.mu.Lock()
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	s.mu.Unlock()
}

// PID returns the underlying process id.
func (s *Session) PID() int { return s.pid }

// Dir returns the working directory the agent was started in.
func (s *Session) Dir() string {
	if s.cmd.Dir == "" {
		return ""
	}
	return s.cmd.Dir
}

// StartTime returns when the session was spawned.
func (s *Session) StartTime() time.Time { return s.started }

// Done is closed as soon as the process has exited.
func (s *Session) Done() <-chan struct{} { return s.done }

// IsAlive reports whether the process is still running and the session open.
func (s *Session) IsAlive() bool {
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

// Sniff returns the current visible screen text and whether it changed since
// the previous Sniff/Feed.
func (s *Session) Sniff() (string, bool) {
	s.scrn.mu.Lock()
	defer s.scrn.mu.Unlock()
	s.scrn.persistScroll()
	text := s.TextLocked()
	changed := s.scrn.dirty
	s.scrn.dirty = false
	return text, changed
}

// TextLocked renders the current screen; the caller must hold the screen lock.
func (s *Session) TextLocked() string {
	return s.scrn.TextNoLock()
}

// Frame returns the current visible screen text.
func (s *Session) Frame() string {
	s.scrn.mu.Lock()
	defer s.scrn.mu.Unlock()
	return s.scrn.TextNoLock()
}

// SniffPNG renders the current screen as a PNG and reports whether the screen
// changed since the previous Sniff/SniffPNG. The image lock is held for the
// whole render so a frame never mixes partial draws.
func (s *Session) SniffPNG() ([]byte, bool, error) {
	s.scrn.mu.Lock()
	defer s.scrn.mu.Unlock()
	s.scrn.persistScroll()
	img, err := s.scrn.PNGNoLock()
	changed := s.scrn.dirty
	s.scrn.dirty = false
	return img, changed, err
}

// ViewCount returns the number of lines in the full scrollback view: the lines
// that already scrolled off plus the rows currently on screen.
func (s *Session) ViewCount() int {
	s.scrn.mu.Lock()
	defer s.scrn.mu.Unlock()
	return len(s.scrn.viewRows())
}

// ViewRows returns up to n view rows ending just before index end, so the
// newest view is the one a reader starts from (end == ViewCount). An end below
// n is clamped to the start of the view, which is how the oldest lines are
// reached. Rows keep the colors the TUI drew them with.
func (s *Session) ViewRows(end, n int) []Row {
	if n <= 0 {
		return nil
	}
	s.scrn.mu.Lock()
	defer s.scrn.mu.Unlock()
	all := s.scrn.viewRows()
	if end > len(all) {
		end = len(all)
	}
	if end < 0 {
		end = 0
	}
	start := end - n
	if start < 0 {
		start = 0
	}
	out := make([]Row, end-start)
	copy(out, all[start:end])
	return out
}

// Write sends raw bytes to the agent's TUI stdin.
func (s *Session) Write(data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrNotRunning
	}
	_, err := s.master.Write(data)
	return err
}

// Close terminates the TUI gracefully: Ctrl-C through the PTY first, then
// SIGINT, then SIGKILL shortly after.
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	_, _ = s.master.Write([]byte("\x03"))
	time.Sleep(300 * time.Millisecond)
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Signal(os.Interrupt)
	}
	time.AfterFunc(800*time.Millisecond, func() {
		if s.IsAlive() && s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
	})
	_ = s.master.Close()
	return nil
}
