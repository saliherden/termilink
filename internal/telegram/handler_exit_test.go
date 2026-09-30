package telegram

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
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
		Runner:     terminal.NewRunner("/bin/zsh", 1<<20),
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

// TestConcurrentCommandsInOneSession is the regression for the run lock.
//
// The bot library dispatches every update on its own goroutine
// (bot.ProcessUpdate does `go r(ctx, b, upd)` unless WithNotAsyncHandlers is
// set, and production does not set it), so two messages from one chat enter
// runCommand at the same time. The session's Active flag cannot arbitrate
// that: reading it and setting it are two separate steps with an audit write
// and a shell open in between, and both goroutines sail through.
//
// Letting two commands share one pty is not a cosmetic bug. Shell.curStop is
// a single field that each ExecCommand overwrites, so the first command's defer
// sets it to nil while the second is still running — after which /stop has
// nothing to stop and Ctrl-C goes to whichever command the field happens to
// name.
func TestConcurrentCommandsInOneSession(t *testing.T) {
	h, _ := newExitHandler(t, 4000)
	f := newFakeTelegram(t)

	const n = 4
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			h.handle(context.Background(), f.bot, 190, testAgentOwner,
				fmt.Sprintf("sleep 0.4; echo marker-%d", i))
		}(i)
	}
	wg.Wait()

	ran := 0
	for i := 0; i < n; i++ {
		if f.saw(fmt.Sprintf("marker-%d", i)) {
			ran++
		}
	}
	if ran != 1 {
		t.Fatalf("%d of %d commands ran against one session, want exactly 1; owner saw: %v",
			ran, n, f.messages())
	}
	rejected := 0
	for _, m := range f.messages() {
		if strings.Contains(m, "a command is already running") {
			rejected++
		}
	}
	if rejected != n-1 {
		t.Fatalf("rejections = %d, want %d; owner saw: %v", rejected, n-1, f.messages())
	}
}

// A refusal the owner is told about has to be in the audit trail like any
// other. The rejection happens before the command is recorded, so without this
// it left no trace at all.
func TestBusyRejectionIsAudited(t *testing.T) {
	h, auditPath := newExitHandler(t, 4000)
	f := newFakeTelegram(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.handle(context.Background(), f.bot, 192, testAgentOwner, "sleep 1; echo first")
	}()
	time.Sleep(300 * time.Millisecond)
	h.handle(context.Background(), f.bot, 192, testAgentOwner, "echo second")
	<-done

	var found bool
	for _, e := range readAuditEntries(t, auditPath) {
		if e.Action == audit.ActionBusySession {
			found = true
			if e.Cmd != "echo second" {
				t.Fatalf("busy_session entry recorded cmd %q, want the rejected command", e.Cmd)
			}
		}
		if e.Action == audit.ActionCommandResult && strings.Contains(e.Cmd, "second") {
			t.Fatalf("the rejected command still ran and recorded a result: %+v", e)
		}
	}
	if !found {
		t.Fatalf("the rejection was not audited: %+v", readAuditEntries(t, auditPath))
	}
}

// The run lock is held for the whole command, so the fix must not make /stop
// wait for that lock — an interrupt that blocks behind the command it is meant
// to interrupt is no interrupt at all. /stop reads the session flag and calls
// shell.Stop, neither of which takes the run lock.
func TestStopInterruptsWhileRunLockIsHeld(t *testing.T) {
	h, _ := newExitHandler(t, 4000)
	f := newFakeTelegram(t)

	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		h.handle(context.Background(), f.bot, 191, testAgentOwner, "sleep 20; echo never-printed")
	}()

	// The sleep, not a poll of st.Active: reading that field from here would be
	// the same unsynchronised access the fix is about.
	time.Sleep(500 * time.Millisecond)
	h.handle(context.Background(), f.bot, 191, testAgentOwner, "/stop")

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("/stop did not return the command within 10s; the run lock is blocking the interrupt")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("command took %s, so it ran to completion instead of being interrupted", elapsed)
	}
	if !f.saw("Command interrupted") {
		t.Fatalf("the command was not reported as interrupted; owner saw: %v", f.messages())
	}
}
