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
	warned   bool
}

// Open opens (creating if needed) the audit log at path. Pass "" to use the
// default location. Use a nil *Logger to disable auditing.
func Open(path string) (*Logger, error) {
	return OpenWithMax(path, 0)
}

// OpenWithMax is like Open but rotates the log to "<path>.1" before appending
// once the file grows past maxBytes (0 disables rotation). Rotation keeps a
// single backup and never blocks the caller.
func OpenWithMax(path string, maxBytes int64) (*Logger, error) {
	if path == "" {
		path = DefaultPath()
	}
	if path == "" {
		return nil, errors.New("audit: no default path available")
	}
	if maxBytes < 0 {
		maxBytes = 0
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("audit: open %s: %w", path, err)
	}
	return &Logger{path: path, f: f, maxBytes: maxBytes}, nil
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
// it has grown past maxBytes. Called with l.mu held.
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
	if err := os.Rename(l.path, l.path+".1"); err != nil {
		l.note("rotate rename: " + err.Error())
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		l.note("rotate reopen: " + err.Error())
		return
	}
	l.f = f
}

func (l *Logger) note(msg string) {
	if l.warned {
		return
	}
	l.warned = true
	fmt.Fprintln(os.Stderr, "audit: "+msg)
}
