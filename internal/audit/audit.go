package audit

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

const defaultFileName = "audit.log"

// DefaultPath returns the default audit log location
// (~/.termilink/audit.log).
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".termilink", defaultFileName)
}

// PathFor maps a configured audit_log value to a concrete file path. It is the
// single place that interprets the setting, so the running agent and the
// `status` command cannot disagree about where the log is. The second return
// is false when auditing is disabled ("off"); the first can be empty for an
// empty value when there is no home directory, which the caller must treat as
// an error rather than a disabled log.
func PathFor(configured string) (path string, enabled bool) {
	if configured == "off" {
		return "", false
	}
	if configured == "" {
		return DefaultPath(), true
	}
	return configured, true
}

// Action identifiers stored in every entry.
const (
	ActionCommand       = "command"
	ActionCommandResult = "command_result"
	ActionAccessDenied  = "access_denied"
	ActionVetBlocked    = "vet_blocked"
	ActionBusySession   = "busy_session"
	ActionProjectSwitch = "project_switch"
	ActionApprovalReq   = "approval_requested"
	ActionApprovalOK    = "approval_approved"
	ActionApprovalNo    = "approval_rejected"
	ActionApprovalBlock = "approval_blocked"
	ActionApprovalTimed = "approval_timeout"
	ActionInput         = "input"
	ActionStop          = "stop"
	ActionExit          = "exit"
	ActionFileGet       = "file_get"
	ActionFileUpload    = "file_upload"
	ActionFileLink      = "file_link"
	ActionAgentStart    = "agent_start"
	ActionAgentInput    = "agent_input"
	ActionAgentStop     = "agent_stop"
	ActionAgentHistory  = "agent_history"
	ActionAgentSession  = "agent_session"
	ActionAgentError    = "agent_error"
	ActionPanic         = "panic"
)

// Entry is a single append-only audit record, serialized as one JSON line.
type Entry struct {
	Time   time.Time `json:"time"`
	UserID int64     `json:"user_id"`
	Owner  bool      `json:"owner"`
	ChatID int64     `json:"chat_id"`
	Action string    `json:"action"`
	Cmd    string    `json:"cmd,omitempty"`
	Detail string    `json:"detail,omitempty"`
	OK     *bool     `json:"ok,omitempty"`
	DurMS  int64     `json:"dur_ms,omitempty"`
	Err    string    `json:"err,omitempty"`
}

// Bool returns a pointer to v for use in Entry.OK.
func Bool(v bool) *bool { return &v }

var (
	secretRe = regexp.MustCompile(`(?i)\b(password|passwd|pass|api[_-]?key|token|secret|auth(?:orization)?[_-]?token)\s*[=:]\s*[^\s,;"']+`)
	bearerRe = regexp.MustCompile(`(?i)\b(bearer)\s+[a-z0-9._~+/=-]+`)
	redacted = "***redacted***"
)

// Redact replaces obvious secret values in a raw command with a placeholder so
// credentials never reach the audit file. The executed command is unaffected.
func Redact(raw string) string {
	s := bearerRe.ReplaceAllString(raw, "${1} "+redacted)
	s = secretRe.ReplaceAllString(s, "${1}="+redacted)
	return s
}

// Logger writes append-only JSON lines to an audit file. Each Logger owns the
// file descriptor and is safe for concurrent use.
type Logger struct {
	mu       sync.Mutex
	path     string
	f        *os.File
	maxBytes int64 // 0 = unlimited; when exceeded the file is rotated to .1
	keep     int   // how many rotated archives to keep; always >= 1
	warned   bool
}

// Open opens (creating if needed) the audit log at path. Pass "" to use the
// default location. Use a nil *Logger to disable auditing.
func Open(path string) (*Logger, error) {
	return OpenWithMax(path, 0)
}

// OpenWithMax is like Open but rotates the log before appending once the file
// grows past maxBytes (0 disables rotation), keeping a single backup. It is
// retained for callers that do not care about history; new code should use
// OpenWithOptions.
func OpenWithMax(path string, maxBytes int64) (*Logger, error) {
	return OpenWithOptions(path, maxBytes, 1)
}

// OpenWithOptions opens the audit log with a rotation size and an archive
// retention count. When the log grows past maxBytes, it is shifted to
// "<path>.1" and older archives move up to "<path>.2", "<path>.3" and so on;
// the archive at ".<keep>" is deleted to make room. keep is clamped to at least
// 1, which is what rotation did before this parameter existed: every rotation
// replaced ".1", so the log never held more than one cap's worth of history.
func OpenWithOptions(path string, maxBytes int64, keep int) (*Logger, error) {
	if path == "" {
		path = DefaultPath()
	}
	if path == "" {
		return nil, errors.New("audit: no default path available")
	}
	if maxBytes < 0 {
		maxBytes = 0
	}
	if keep < 1 {
		keep = 1
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("audit: open %s: %w", path, err)
	}
	return &Logger{path: path, f: f, maxBytes: maxBytes, keep: keep}, nil
}

// Path returns the resolved audit file location.
func (l *Logger) Path() string { return l.path }

// Close flushes and closes the audit file.
func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	return l.f.Close()
}

// Audit appends one entry. A nil receiver and write errors (reported once to
// stderr) are tolerated so auditing never disrupts the agent.
func (l *Logger) Audit(e Entry) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.maybeRotate()
	data, err := json.Marshal(e)
	if err != nil {
		l.note("marshal: " + err.Error())
		return
	}
	buf := make([]byte, 0, len(data)+1)
	buf = append(buf, data...)
	buf = append(buf, '\n')
	if _, err := l.f.Write(buf); err != nil {
		l.note("write: " + err.Error())
	}
}

// maybeRotate moves the current file to "<path>.1" and starts a fresh one when
// it has grown past maxBytes, first shifting the older archives up so the
// retention count is honoured. Called with l.mu held.
func (l *Logger) maybeRotate() {
	if l.maxBytes <= 0 {
		return
	}
	info, err := l.f.Stat()
	if err != nil {
		l.note("stat: " + err.Error())
		return
	}
	if info.Size() <= l.maxBytes {
		return
	}
	if err := l.f.Close(); err != nil {
		l.note("close before rotate: " + err.Error())
		return
	}
	l.shiftArchives()
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		l.note("rotate reopen: " + err.Error())
		return
	}
	l.f = f
}

// shiftArchives makes room for the incoming rotation: the current log takes
// the .1 slot and the older archives move up by one, with anything past the
// retention count deleted. Called with l.mu held and the current file already
// closed.
func (l *Logger) shiftArchives() {
	// Trim first, before any rename. Every destination in the shift below is
	// then guaranteed to be free, which is what keeps it working on Windows
	// where renaming onto an existing file fails. Trimming here rather than
	// deleting only ".<keep>" is also what makes lowering audit_keep take
	// effect: archives left by a configuration that kept more of them would
	// otherwise sit on disk forever, since nothing would ever shift past them.
	for i := l.keep + 1; ; i++ {
		if _, err := os.Stat(l.archive(i)); err != nil {
			break
		}
		_ = os.Remove(l.archive(i))
	}
	for i := l.keep - 1; i >= 1; i-- {
		src := l.archive(i)
		if _, err := os.Stat(src); err != nil {
			// A gap in the chain just means less history than the retention
			// count allows. The index encodes age, so a hole stays a hole
			// rather than being compacted, which would renumber archives
			// behind the caller's back.
			continue
		}
		if err := os.Rename(src, l.archive(i+1)); err != nil {
			l.note("rotate shift: " + err.Error())
		}
	}
	if err := os.Rename(l.path, l.archive(1)); err != nil {
		l.note("rotate rename: " + err.Error())
	}
}

// archive returns the path of the nth rotated copy, where 1 is the newest.
func (l *Logger) archive(n int) string { return fmt.Sprintf("%s.%d", l.path, n) }

func (l *Logger) note(msg string) {
	if l.warned {
		return
	}
	l.warned = true
	fmt.Fprintln(os.Stderr, "audit: "+msg)
}
