package telegram

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/saliherden/termilink/internal/audit"
	"github.com/saliherden/termilink/internal/config"
	"github.com/saliherden/termilink/internal/security"
	"github.com/saliherden/termilink/internal/session"
	"github.com/saliherden/termilink/internal/terminal"
)

// newExitHandler wires a handler with the pieces runCommand needs: a real zsh
// runner, an audit log in a temp dir, and a session manager whose state file
// also lives in a temp dir so a test cannot rewrite the real ~/.termilink.
func newExitHandler(t *testing.T, maxMsgLen int) (*Handler, string) {
	t.Helper()
	auditPath := filepath.Join(t.TempDir(), "audit.log")
	l, err := audit.Open(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(Options{
		Authorizer: security.New(testAgentOwner, []int64{testAgentOwner}),
		Runner:     terminal.NewRunner("/bin/zsh", 20*time.Second, 1<<20),
		Sessions:   session.NewManagerWithStateFile(filepath.Join(t.TempDir(), "state.json")),
		Projects:   map[string]config.ProjectConfig{"app": {Path: t.TempDir()}},
		MaxMsgLen:  maxMsgLen,
		Audit:      l,
	})
	t.Cleanup(func() {
		h.Close()
		_ = l.Close()
	})
	return h, auditPath
}

// TestRunCommandReportsRealExitCode is the regression: a command that ran and
// exited non-zero used to be reported as "exited with code 0", because the
// PTY path returns no Go error for it and the frame carried no status.
func TestRunCommandReportsRealExitCode(t *testing.T) {
	h, _ := newExitHandler(t, 4000)
	f := newFakeTelegram(t)

	h.handle(context.Background(), f.bot, 181, testAgentOwner, "false")

	if !f.saw("exited with code 1") {
		t.Fatalf("a failing command was not reported as failing; owner saw: %v", f.messages())
	}
	if f.saw("exited with code 0") {
		t.Fatalf("a failing command claimed exit code 0; owner saw: %v", f.messages())
	}
}

// The success path is the control: the fix must not turn every command into a
// failure either.
func TestRunCommandReportsZeroOnSuccess(t *testing.T) {
	h, _ := newExitHandler(t, 4000)
	f := newFakeTelegram(t)

	h.handle(context.Background(), f.bot, 182, testAgentOwner, "echo still-fine")

	if !f.saw("still-fine") {
		t.Fatalf("output missing; owner saw: %v", f.messages())
	}
	if !f.saw("exited with code 0") {
		t.Fatalf("a successful command did not report code 0; owner saw: %v", f.messages())
	}
}

// TestRunCommandLongOutputCarriesExitCode covers the other site that had a
// hardcoded zero: when the output is too long, the status is only ever shown on
// the preview message, so losing it there loses it entirely.
//
// The failing command runs in a subshell. A bare `exit 3` would end the
// persistent interactive shell — which is the shell itself, not a child — and
// the turn would end as "shell exited while running command" instead.
func TestRunCommandLongOutputCarriesExitCode(t *testing.T) {
	h, _ := newExitHandler(t, 200)
	f := newFakeTelegram(t)

	h.handle(context.Background(), f.bot, 183, testAgentOwner,
		`for i in $(seq 1 40); do echo "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa line $i"; done; (exit 3)`)

	if !f.saw("exited with code 3") {
		t.Fatalf("the long-output preview dropped the real status; owner saw: %v", f.messages())
	}
	if f.saw("exited with code 0") {
		t.Fatalf("the long-output preview claimed exit code 0; owner saw: %v", f.messages())
	}
}

// TestRunCommandAuditsFailure pins the same truth in the audit trail, which
// used to record ok=true for anything that did not time out or get interrupted.
func TestRunCommandAuditsFailure(t *testing.T) {
	h, auditPath := newExitHandler(t, 4000)
	f := newFakeTelegram(t)

	h.handle(context.Background(), f.bot, 184, testAgentOwner, "false")

	var found bool
	for _, e := range readAuditEntries(t, auditPath) {
		if e.Action != audit.ActionCommandResult {
			continue
		}
		found = true
		if e.OK == nil {
			t.Fatalf("command_result entry has no ok field: %+v", e)
		}
		if *e.OK {
			t.Fatalf("audit recorded a failed command as ok: %+v", e)
		}
	}
	if !found {
		t.Fatalf("no command_result entry was written: %+v", readAuditEntries(t, auditPath))
	}
}
