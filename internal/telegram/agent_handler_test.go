package telegram

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tg "github.com/go-telegram/bot"

	"github.com/saliherden/termilink/internal/audit"
	"github.com/saliherden/termilink/internal/config"
	"github.com/saliherden/termilink/internal/security"
	"github.com/saliherden/termilink/internal/session"
	"github.com/saliherden/termilink/internal/terminal"
)

const testAgentOwner int64 = 222

// testAgentHandler builds a Handler with an owner, one project named "app" and
// the given agent command, wired to a temp audit log.
func testAgentHandler(t *testing.T, command string) (*Handler, string) {
	t.Helper()
	dir := t.TempDir()
	auditPath := filepath.Join(t.TempDir(), "audit.log")
	l, err := audit.Open(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(Options{
		Authorizer: security.New(testAgentOwner, []int64{testAgentOwner}),
		Sessions:   session.NewManager(),
		Projects:   map[string]config.ProjectConfig{"app": {Path: dir, Commands: map[string]string{}}},
		Agent:      config.AgentConfig{Enabled: true, Command: command},
		Audit:      l,
	})
	t.Cleanup(func() {
		h.Close()
		_ = l.Close()
	})
	return h, auditPath
}

// writeAgentFixture creates a simple TUI-like shell loop that echoes every
// line it reads, so PTY input can be observed on screen.
func writeAgentFixture(t *testing.T) string {
	t.Helper()
	script := `#!/bin/sh
printf 'ready> '
while read -r line; do
	printf 'echo:%s\n' "$line"
done
`
	path := filepath.Join(t.TempDir(), "agent.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func selectTestProject(t *testing.T, h *Handler, chatID int64) *session.State {
	t.Helper()
	st := h.sessions.Ensure(strconv.FormatInt(chatID, 10))
	h.closeShellFor(st.ID)
	st.Cwd = h.projects["app"].Path
	st.Project = "app"
	return st
}

func TestAgentStartWorkerDenied(t *testing.T) {
	h, auditPath := testAgentHandler(t, writeAgentFixture(t))
	selectTestProject(t, h, 100)

	// A non-owner's bare `agent` message must never open a session.
	h.handle(context.Background(), nil, 100, 9999, "agent fix the bug")

	if run := h.agentSessionFor(100); run != nil {
		h.agentCleanup(100, run)
		t.Fatal("worker owned an agent session")
	}
	entries := readAuditEntries(t, auditPath)
	if len(entries) != 1 {
		t.Fatalf("want 1 audit entry, got %d", len(entries))
	}
	if entries[0].Action != audit.ActionAccessDenied {
		t.Fatalf("action = %s, want access_denied", entries[0].Action)
	}
}

func TestAgentStartRequiresProject(t *testing.T) {
	h, _ := testAgentHandler(t, writeAgentFixture(t))
	// No project selected on the session.
	h.handle(context.Background(), nil, 101, testAgentOwner, "agent hi")
	if run := h.agentSessionFor(101); run != nil {
		h.agentCleanup(101, run)
		t.Fatal("agent must require a project")
	}
}

func TestAgentStartNotFound(t *testing.T) {
	h, _ := testAgentHandler(t, "/no/such/agent-binary")
	selectTestProject(t, h, 102)
	h.handle(context.Background(), nil, 102, testAgentOwner, "agent hi")
	if run := h.agentSessionFor(102); run != nil {
		h.agentCleanup(102, run)
		t.Fatal("agent started despite missing binary")
	}
}

func TestAgentLifecycle(t *testing.T) {
	h, auditPath := testAgentHandler(t, writeAgentFixture(t))
	st := selectTestProject(t, h, 103)

	// Empty prompt: opens the TUI and the relay starts.
	h.handle(context.Background(), nil, 103, testAgentOwner, "agent ")

	run := h.agentSessionFor(103)
	if run == nil {
		t.Fatal("owner agent session not registered")
	}
	defer h.agentCleanup(103, run)

	if !run.sess.IsAlive() {
		t.Fatal("agent session not alive")
	}
	if run.dir != h.projects["app"].Path {
		t.Fatalf("agent dir = %q, want %q", run.dir, h.projects["app"].Path)
	}
	if run.chatID != 103 {
		t.Fatalf("agent chat = %d, want 103", run.chatID)
	}

	// A plain chat message is typed into the agent's input box.
	h.handle(context.Background(), nil, 103, testAgentOwner, "hello agent")
	waitAgentFrame(t, run, "echo:hello agent")

	// Sniff read-clears: the frame after output is dirty, the next is clean.
	_, changed := run.sess.Sniff()
	if !changed {
		t.Fatal("expected a dirty frame after output")
	}

	// A worker must not be able to write into the running session.
	h.handle(context.Background(), nil, 103, 9999, "rm -rf /")
	if strings.Contains(run.sess.Frame(), "rm -rf /") {
		t.Fatal("worker input reached the agent TUI")
	}

	// /agent stop tears the run down and marks it stopped.
	h.handleCommand(context.Background(), nil, 103, testAgentOwner, st, "/agent stop")
	if h.agentSessionFor(103) != nil {
		t.Fatal("agent session still registered after stop")
	}
	if run.sess.IsAlive() {
		t.Fatal("agent session still alive after stop")
	}

	entries := readAuditEntries(t, auditPath)
	var started, stopped bool
	for _, e := range entries {
		switch e.Action {
		case audit.ActionAgentStart:
			started = true
		case audit.ActionAgentStop:
			stopped = true
		case audit.ActionAgentInput, audit.ActionAccessDenied:
		default:
			t.Fatalf("unexpected audit action %s", e.Action)
		}
	}
	if !started || !stopped {
		t.Fatalf("missing agent_start/agent_stop audit entries: %+v", entries)
	}
}

func TestAgentStatusWithoutSession(t *testing.T) {
	h, _ := testAgentHandler(t, writeAgentFixture(t))
	h.handleCommand(context.Background(), nil, 104, testAgentOwner,
		h.sessions.Ensure(strconv.FormatInt(104, 10)), "/agent status")
	if h.agentSessionFor(104) != nil {
		h.agentCleanup(104, h.agentSessionFor(104))
		t.Fatal("status on an empty handler opened a session")
	}
}

func TestGetRouteReadsBeforeAgent(t *testing.T) {
	h, _ := testAgentHandler(t, writeAgentFixture(t))
	selectTestProject(t, h, 105)
	h.handle(context.Background(), nil, 105, testAgentOwner, "agent ")
	run := h.agentSessionFor(105)
	if run == nil {
		t.Fatal("agent session not registered")
	}
	defer h.agentCleanup(105, run)

	// `get ...` is routed by handle() before the agent session intercept, so it
	// never reaches the TUI as input.
	h.handle(context.Background(), nil, 105, testAgentOwner, "get config.yaml")
	if strings.Contains(run.sess.Frame(), "config.yaml") {
		t.Fatal("get message was typed into the agent TUI")
	}
}

// waitSaw polls until substr shows up in what the bot sent. Command results
// arrive asynchronously over the PTY, so a test that asserts on them would
// otherwise be guessing how long the shell needs.
func waitSaw(t *testing.T, f *fakeTelegram, substr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if f.saw(substr) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("bot never sent %q; it sent: %v", substr, f.messages())
}

func waitAgentFrame(t *testing.T, run *agentRun, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if strings.Contains(run.sess.Frame(), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %q, frame=%q", want, run.sess.Frame())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAgentHistoryViewBounds(t *testing.T) {
	if agentHistoryStep != 36 {
		t.Fatalf("step = %d, want 36 (a page minus a 4-line overlap)", agentHistoryStep)
	}
	// A view longer than one page can scroll both ways and never runs off.
	if got := agentHistoryViewBounds(100, 100, -1); got != 64 {
		t.Fatalf("up from the newest = %d, want 64", got)
	}
	if got := agentHistoryViewBounds(100, 64, 1); got != 100 {
		t.Fatalf("down = %d, want the newest position 100", got)
	}
	// Near the top the window shrinks instead of being forced to a full page.
	// This is the fix for the "one /agent up hides the newest 17 lines" bug:
	// at 57 lines the reader used to clamp 21 up to 40, skipping 41-57.
	if got := agentHistoryViewBounds(57, 57, -1); got != 21 {
		t.Fatalf("up from the newest at 57 lines = %d, want 21 (partial page)", got)
	}
	if got := agentHistoryViewBounds(57, 21, -1); got != 1 {
		t.Fatalf("up at the very top = %d, want 1", got)
	}
	if got := agentHistoryViewBounds(57, 1, -1); got != 1 {
		t.Fatalf("up past the first line = %d, want to hold at 1", got)
	}
	// The top of a long view is a full page from the first line.
	if got := agentHistoryTop(100); got != 40 {
		t.Fatalf("top of 100 lines = %d, want 40", got)
	}
	if got := agentHistoryTop(20); got != 20 {
		t.Fatalf("top of a 20-line view = %d, want 20", got)
	}
	// A transcript shorter than a page is a single view that cannot scroll.
	if got := agentHistoryViewBounds(20, 20, -1); got != 1 {
		t.Fatalf("short view scrolled to %d, want 1", got)
	}
	if got := agentHistoryViewBounds(0, 0, -1); got != 0 {
		t.Fatalf("empty view = %d, want 0", got)
	}
	// Every position stays inside the view.
	for end := 0; end <= 200; end++ {
		got := agentHistoryViewBounds(200, end, -1)
		if got < 1 || got > 200 {
			t.Fatalf("bounds(%d) = %d, out of range", end, got)
		}
		if down := agentHistoryViewBounds(200, end, 1); down < 1 || down > 200 {
			t.Fatalf("down bounds(%d) = %d, out of range", end, down)
		}
	}
}

func TestAgentHistoryScrollCoversEveryLine(t *testing.T) {
	// Walking up from the newest line to the top must show every line at least
	// once: a scrollback that skips content is worse than no scrollback.
	for _, total := range []int{1, 20, 40, 41, 63, 100, 137, 200, 777, 2000} {
		covered := make([]bool, total)
		end := total
		covered[0] = true
		for i := 0; i < total+agentHistoryPage; i++ { // bounded: must converge
			start := end - agentHistoryPage
			if start < 0 {
				start = 0
			}
			for j := start; j < end; j++ {
				covered[j] = true
			}
			next := agentHistoryViewBounds(total, end, -1)
			if next == end {
				break
			}
			end = next
		}
		for i, c := range covered {
			if !c {
				t.Fatalf("total=%d: line %d is never shown while scrolling up", total, i)
			}
		}
	}
}

// TestAgentHistoryUpDoesNotSkipLines is the regression test for the reported
// bug: at 57 lines the first `/agent up` jumped from the newest position straight
// to 40, so lines 41-57 were never shown again without scrolling back down.
func TestAgentHistoryUpDoesNotSkipLines(t *testing.T) {
	const total = 57
	seen := map[int]bool{}
	end := total
	for i := 0; i < total+agentHistoryPage; i++ {
		start := end - agentHistoryPage
		if start < 0 {
			start = 0
		}
		for j := start; j < end; j++ {
			seen[j] = true
		}
		next := agentHistoryViewBounds(total, end, -1)
		if next == end {
			break
		}
		end = next
	}
	// The very first /agent up must not be the jump that loses the tail.
	if first := agentHistoryViewBounds(total, total, -1); first != 21 {
		t.Fatalf("first up at %d lines = %d, want 21", total, first)
	}
	for i := 0; i < total; i++ {
		if !seen[i] {
			t.Fatalf("line %d is skipped while scrolling up from the newest", i)
		}
	}
}

func TestAgentKeyAliasCommands(t *testing.T) {
	h, _ := testAgentHandler(t, writeAgentFixture(t))
	st := selectTestProject(t, h, 107)

	// Without a live session a slash word is just an unknown command: there is
	// no TUI to receive it, and nothing should be started by one.
	h.handleCommand(context.Background(), nil, 107, testAgentOwner, st, "/up")
	if h.agentSessionFor(107) != nil {
		t.Fatal("/up started a session")
	}

	h.handle(context.Background(), nil, 107, testAgentOwner, "agent ")
	run := h.agentSessionFor(107)
	if run == nil {
		t.Fatal("agent session not registered")
	}
	defer h.agentCleanup(107, run)
	waitAgentFrame(t, run, "ready>")

	// These are no longer commands of their own: an unknown slash word with an
	// agent running is typed in, and MapInput still turns the key names into
	// real key bytes.
	h.handle(context.Background(), nil, 107, testAgentOwner, "/enter")
	waitAgentFrame(t, run, "echo:")

	// A path-like word is the case that used to be lost: it must reach the TUI
	// as literal text instead of dying as "Unknown command".
	h.handle(context.Background(), nil, 107, testAgentOwner, "/update.sh")
	waitAgentFrame(t, run, "echo:/update.sh")

	// Text the user actually meant to send keeps its own trailing words.
	h.handle(context.Background(), nil, 107, testAgentOwner, "/etc/hosts has the entry")
	waitAgentFrame(t, run, "echo:/etc/hosts has the entry")

	// The bare key names are unchanged.
	for _, key := range []string{"up", "down", "esc", "tab", "pageup"} {
		h.handle(context.Background(), nil, 107, testAgentOwner, key)
		if !run.sess.IsAlive() {
			t.Fatalf("%q killed the session", key)
		}
	}
}

func TestAgentKeyInputWorkerDenied(t *testing.T) {
	h, auditPath := testAgentHandler(t, writeAgentFixture(t))
	_ = selectTestProject(t, h, 111)
	h.handle(context.Background(), nil, 111, testAgentOwner, "agent ")
	run := h.agentSessionFor(111)
	if run == nil {
		t.Fatal("agent session not registered")
	}
	defer h.agentCleanup(111, run)
	waitAgentFrame(t, run, "ready>")

	// Typing into the session is owner-only, and the pass-through for unknown
	// slash words must not become a way around that. /enter is the probe
	// because it is visible in the frame: if those bytes were written, the
	// fixture echoes the completed line.
	h.handle(context.Background(), nil, 111, 9999, "/enter")
	h.handle(context.Background(), nil, 111, 9999, "/update.sh")
	time.Sleep(100 * time.Millisecond)
	if strings.Contains(run.sess.Frame(), "echo:") {
		t.Fatalf("worker reached the owner's TUI: %q", run.sess.Frame())
	}

	var denied bool
	for _, e := range readAuditEntries(t, auditPath) {
		if e.Action == audit.ActionAccessDenied {
			denied = true
		}
	}
	if !denied {
		t.Fatalf("worker input was not denied/audited: %+v", readAuditEntries(t, auditPath))
	}
}

func TestAgentUnknownSlashWordIsNotACommand(t *testing.T) {
	h, _ := testAgentHandler(t, writeAgentFixture(t))
	st := selectTestProject(t, h, 108)

	// With no agent running an unrecognized slash word is still refused, so
	// typos in real commands keep telling you so.
	h.handleCommand(context.Background(), nil, 108, testAgentOwner, st, "/update.sh")
	if h.agentSessionFor(108) != nil {
		t.Fatal("/update.sh started a session")
	}
}

func TestAgentExitClosesSession(t *testing.T) {
	h, auditPath := testAgentHandler(t, writeAgentFixture(t))
	st := selectTestProject(t, h, 109)
	h.handle(context.Background(), nil, 109, testAgentOwner, "agent ")
	run := h.agentSessionFor(109)
	if run == nil {
		t.Fatal("agent session not registered")
	}
	waitAgentFrame(t, run, "ready>")

	// /agent exit is the second name for /agent stop.
	h.handleCommand(context.Background(), nil, 109, testAgentOwner, st, "/agent exit")
	if h.agentSessionFor(109) != nil {
		t.Fatal("/agent exit left the session registered")
	}
	// The audit log records which word was used.
	var found bool
	for _, e := range readAuditEntries(t, auditPath) {
		if e.Action == audit.ActionAgentStop && e.Cmd == "exit" {
			found = true
		}
	}
	if !found {
		t.Fatalf("/agent exit not audited with its verb: %+v", readAuditEntries(t, auditPath))
	}

	// Closing it twice is harmless, and the history reader has nothing to read.
	h.handleCommand(context.Background(), nil, 109, testAgentOwner, st, "/agent exit")
	h.handleCommand(context.Background(), nil, 109, testAgentOwner, st, "/agent history")
}

func TestAgentHistoryExitDropsHints(t *testing.T) {
	h, _ := testAgentHandler(t, writeAgentFixture(t))
	st := selectTestProject(t, h, 110)
	h.handle(context.Background(), nil, 110, testAgentOwner, "agent ")
	run := h.agentSessionFor(110)
	if run == nil {
		t.Fatal("agent session not registered")
	}
	defer h.agentCleanup(110, run)
	waitAgentFrame(t, run, "ready>")

	for i := 0; i < 50; i++ {
		_ = run.sess.Write([]byte(fmt.Sprintf("line%02d\r", i)))
		time.Sleep(5 * time.Millisecond)
		run.sess.Sniff()
	}
	total := run.sess.ViewCount()
	if total <= agentHistoryPage {
		t.Fatalf("view has %d lines, want more than one page", total)
	}

	h.handleCommand(context.Background(), nil, 110, testAgentOwner, st, "/agent history")
	if !run.histHintState() {
		t.Fatal("reader opened without hints")
	}

	// The spelled-out form is the readable one and /agent off the short alias.
	// Both only drop the hints, never the image or the session.
	for _, cmd := range []string{"/agent history exit", "/agent off"} {
		h.handleCommand(context.Background(), nil, 110, testAgentOwner, st, "/agent history")
		if !run.histHintState() {
			t.Fatalf("reader opened without hints before %q", cmd)
		}
		h.handleCommand(context.Background(), nil, 110, testAgentOwner, st, cmd)
		if run.histHintState() {
			t.Fatalf("%q left the hints in place", cmd)
		}
		if end, _ := run.histBounds(); end != total {
			t.Fatalf("%q moved the reader to %d, want %d", cmd, end, total)
		}
		if !run.sess.IsAlive() {
			t.Fatalf("%q killed the agent session", cmd)
		}
	}

	// Reopening brings the hints back, so the reader never gets permanently mute.
	h.handleCommand(context.Background(), nil, 110, testAgentOwner, st, "/agent history")
	if !run.histHintState() {
		t.Fatal("reopening the reader did not restore the hints")
	}
}

func TestAgentHistoryScrollback(t *testing.T) {
	h, auditPath := testAgentHandler(t, writeAgentFixture(t))
	st := selectTestProject(t, h, 106)
	h.handle(context.Background(), nil, 106, testAgentOwner, "agent ")
	run := h.agentSessionFor(106)
	if run == nil {
		t.Fatal("agent session not registered")
	}
	defer h.agentCleanup(106, run)
	waitAgentFrame(t, run, "ready>")

	// Push more lines than the 40-row window holds so that content scrolls off
	// and the view grows beyond a single page.
	for i := 0; i < 50; i++ {
		_ = run.sess.Write([]byte(fmt.Sprintf("line%02d\r", i)))
		time.Sleep(5 * time.Millisecond)
		run.sess.Sniff() // drives scrollback capture
	}
	total := run.sess.ViewCount()
	if total <= agentHistoryPage {
		t.Fatalf("view has %d lines, want more than one page; frame=%q", total, run.sess.Frame())
	}

	// Opening the reader starts at the newest line. (With a nil bot no Telegram
	// message id is ever created, so only the position is observable here.)
	h.handleCommand(context.Background(), nil, 106, testAgentOwner, st, "/agent history")
	if end, _ := run.histBounds(); end != total {
		t.Fatalf("reader opened at %d, want the newest line %d", end, total)
	}

	// /agent up scrolls the reader, /agent down comes back.
	h.handleCommand(context.Background(), nil, 106, testAgentOwner, st, "/agent up")
	older, _ := run.histBounds()
	if older >= total {
		t.Fatalf("/agent up did not scroll: position %d of %d", older, total)
	}
	h.handleCommand(context.Background(), nil, 106, testAgentOwner, st, "/agent down")
	if back, _ := run.histBounds(); back != total {
		t.Fatalf("/agent down landed at %d, want %d", back, total)
	}
	// /agent top jumps to the oldest page instead of stepping there.
	h.handleCommand(context.Background(), nil, 106, testAgentOwner, st, "/agent top")
	if top, _ := run.histBounds(); top != agentHistoryPage {
		t.Fatalf("/agent top landed at %d, want %d", top, agentHistoryPage)
	}
	h.handleCommand(context.Background(), nil, 106, testAgentOwner, st, "/agent bottom")
	if last, _ := run.histBounds(); last != total {
		t.Fatalf("/agent bottom landed at %d, want %d", last, total)
	}

	// The bare words belong to the TUI: while the reader is open, "up" is still
	// the arrow key and a stray word is still a prompt. Neither may move the
	// reader, and neither may be swallowed.
	h.handleCommand(context.Background(), nil, 106, testAgentOwner, st, "/agent history")
	before, _ := run.histBounds()
	h.handle(context.Background(), nil, 106, testAgentOwner, "up")
	if now, _ := run.histBounds(); now != before {
		t.Fatalf("bare \"up\" moved the reader from %d to %d", before, now)
	}
	// It went to the agent as the arrow key, not as the literal word.
	if strings.Contains(run.sess.Frame(), "echo:up") {
		t.Fatalf("bare \"up\" was typed as text instead of sent as a key: %q", run.sess.Frame())
	}
	h.handle(context.Background(), nil, 106, testAgentOwner, "newer")
	if now, _ := run.histBounds(); now != before {
		t.Fatalf("stray word moved the reader from %d to %d", before, now)
	}
	// The word itself reached the TUI as a prompt.
	waitAgentFrame(t, run, "newer")
	if after, _ := run.histBounds(); after != before {
		t.Fatalf("reader position changed on a plain message: %d -> %d", before, after)
	}

	// Paging syntax is gone; point the owner at the scroll commands.
	h.handleCommand(context.Background(), nil, 106, testAgentOwner, st, "/agent history 2")
	if !run.sess.IsAlive() {
		t.Fatal("history reads disturbed the running agent")
	}

	// A worker must not open or move the reader.
	h.handleCommand(context.Background(), nil, 106, 9999, st, "/agent history")
	ownerPos, _ := run.histBounds()
	h.handleCommand(context.Background(), nil, 106, 9999, st, "/agent up")
	if now, _ := run.histBounds(); now != ownerPos {
		t.Fatal("worker moved the owner's reader")
	}

	// Hints are on while the reader is open, and /agent off takes them away
	// without moving the reader or leaving the chat.
	h.handleCommand(context.Background(), nil, 106, testAgentOwner, st, "/agent history")
	if !run.histHintState() {
		t.Fatal("reader opened without hints")
	}
	beforeOff, msgBefore := run.histBounds()
	h.handleCommand(context.Background(), nil, 106, testAgentOwner, st, "/agent off")
	if run.histHintState() {
		t.Fatal("/agent off left the hints in place")
	}
	if after, msgAfter := run.histBounds(); after != beforeOff || msgAfter != msgBefore {
		t.Fatalf("/agent off moved the reader: %d/%d -> %d/%d", beforeOff, msgBefore, after, msgAfter)
	}
	if !run.sess.IsAlive() {
		t.Fatal("/agent off killed the agent session")
	}
	// Reopening brings the hints back.
	h.handleCommand(context.Background(), nil, 106, testAgentOwner, st, "/agent history")
	if !run.histHintState() {
		t.Fatal("reopening the reader did not restore the hints")
	}

	// With the session gone there is nothing to read.
	h.agentCleanup(106, run)
	h.handleCommand(context.Background(), nil, 106, testAgentOwner, st, "/agent history")

	var reads, denied int
	for _, e := range readAuditEntries(t, auditPath) {
		switch e.Action {
		case audit.ActionAgentHistory:
			reads++
		case audit.ActionAccessDenied:
			denied++
		}
	}
	if reads == 0 {
		t.Fatalf("no agent_history audit entry recorded: %+v", readAuditEntries(t, auditPath))
	}
	if denied == 0 {
		t.Fatal("worker history read was not denied/audited")
	}
}

// TestRecoverUpdateContainsPanic is the regression test for the silent death of
// the whole bot: the Telegram library dispatches every update on its own
// goroutine, so a panic in any handler stopped the gateway and left the owner
// with an unanswered question. The panic must be contained and recorded.
func TestRecoverUpdateContainsPanic(t *testing.T) {
	h, auditPath := testAgentHandler(t, writeAgentFixture(t))

	var chatID, userID int64 = 101, testAgentOwner
	func() {
		defer h.recoverUpdate(context.Background(), nil, &chatID, &userID)()
		panic("boom")
	}()

	var found bool
	for _, e := range readAuditEntries(t, auditPath) {
		if e.Action == audit.ActionPanic {
			found = true
			if !strings.Contains(e.Cmd, "boom") {
				t.Fatalf("panic audit entry = %q, want it to name the panic value", e.Cmd)
			}
			if e.ChatID != chatID || e.UserID != userID {
				t.Fatalf("panic audit entry lost the update identity: %+v", e)
			}
		}
	}
	if !found {
		t.Fatalf("no %q audit entry recorded: %+v", audit.ActionPanic, readAuditEntries(t, auditPath))
	}
}

// TestRecoverUpdateIgnoresSuccess makes sure the guard is inert on the happy
// path and still runs its bookkeeping.
func TestRecoverUpdateIgnoresSuccess(t *testing.T) {
	h, auditPath := testAgentHandler(t, writeAgentFixture(t))

	var chatID, userID int64 = 101, testAgentOwner
	func() {
		defer h.recoverUpdate(context.Background(), nil, &chatID, &userID)()
	}()

	for _, e := range readAuditEntries(t, auditPath) {
		if e.Action == audit.ActionPanic {
			t.Fatal("a clean update recorded a panic")
		}
	}
}

// TestRelayFrameSurvivesPanic checks the same containment on the relay side: a
// faulty frame must not take down the background goroutine, and with it the
// process, because nothing recovers a goroutine the Telegram library did not
// start. A zero Bot gets past the nil check and a nil session faults on use.
func TestRelayFrameSurvivesPanic(t *testing.T) {
	h, _ := testAgentHandler(t, writeAgentFixture(t))
	run := &agentRun{chatID: 101, bot: &tg.Bot{}, sess: nil, pngMode: true}

	err := h.frameSafely(run)
	if err == nil {
		t.Fatal("a panicking frame reported no error; the process would have died")
	}
	if !strings.Contains(err.Error(), "panic") {
		t.Fatalf("error = %v, want it to name the panic", err)
	}

	// The relay loop keeps going: a nil bot makes each frame a cheap no-op.
	if err := h.frameSafely(&agentRun{chatID: 101}); err != nil {
		t.Fatalf("a nil bot should be a no-op, got %v", err)
	}
}

// TestAgentExitStillAnswersWhenClaimAlreadyTaken is the regression test for a
// silent /agent exit.
//
// Teardown is claimed by whichever goroutine gets there first. If the run is
// already finalised — the process exited on its own and the relay announced it —
// handleAgentStop used to return without sending anything, so the command
// looked like it did nothing at all even though the session was closed and the
// audit log had recorded the stop. Losing the race must still produce an answer.
func TestAgentExitStillAnswersWhenClaimAlreadyTaken(t *testing.T) {
	h, _ := testAgentHandler(t, writeAgentFixture(t))
	f := newFakeTelegram(t)
	st := selectTestProject(t, h, 120)
	h.handle(context.Background(), f.bot, 120, testAgentOwner, "agent ")
	run := h.agentSessionFor(120)
	if run == nil {
		t.Fatal("agent session not registered")
	}
	waitAgentFrame(t, run, "ready>")

	// Another teardown won first: the run is finalised while still registered,
	// which is the state the owner lands in when the agent quits by itself.
	if !run.claim() {
		t.Fatal("first claim should win")
	}
	if h.agentSessionFor(120) != run {
		t.Fatal("expected the stale run to still be registered")
	}

	before := len(f.messages())
	h.handleCommand(context.Background(), f.bot, 120, testAgentOwner, st, "/agent exit")

	if len(f.messages()) == before {
		t.Fatal("/agent exit answered nothing after the run was already finalised")
	}
	last := f.messages()[len(f.messages())-1]
	if !strings.Contains(last, "closed on its own") {
		t.Fatalf("last message = %q, want it to explain the session was already closed", last)
	}
	if h.agentSessionFor(120) != nil {
		t.Fatal("/agent exit left the stale run registered")
	}
}

// TestAgentExitWinsTheRace covers the ordinary path: the command claims the run
// and reports its own closure.
func TestAgentExitWinsTheRace(t *testing.T) {
	h, _ := testAgentHandler(t, writeAgentFixture(t))
	f := newFakeTelegram(t)
	st := selectTestProject(t, h, 121)
	h.handle(context.Background(), f.bot, 121, testAgentOwner, "agent ")
	run := h.agentSessionFor(121)
	if run == nil {
		t.Fatal("agent session not registered")
	}
	waitAgentFrame(t, run, "ready>")

	h.handleCommand(context.Background(), f.bot, 121, testAgentOwner, st, "/agent exit")
	if !f.saw("Agent session closed") {
		t.Fatalf("no closure confirmation; owner saw: %v", f.messages())
	}
	// /agent exit has to post the final screen, exactly like /exit does. It used
	// to send only the text line, which left the last image in the chat as the
	// live relay's scaled-down photo — the owner reported that as a broken
	// screenshot. The screen is text now, so what belongs here is that the text
	// carries the session rather than an empty or torn-down screen.
	if !f.saw("Agent exited") {
		t.Fatalf("/agent exit posted no exit announcement; owner saw: %v", f.messages())
	}
	if !f.saw("ready>") {
		t.Fatalf("/agent exit did not post the final screen text; owner saw: %v", f.messages())
	}
	// The process is really gone, not just unregistered.
	if run.sess.IsAlive() {
		t.Fatal("/agent exit left the agent process running")
	}
}

// TestAgentCleanupSingleWinner pins the invariant the messaging depends on:
// concurrent teardown has exactly one winner, so exactly one of the two paths
// announces an exit.
func TestAgentCleanupSingleWinner(t *testing.T) {
	h, _ := testAgentHandler(t, writeAgentFixture(t))
	run := &agentRun{chatID: 122}
	h.agentMu.Lock()
	h.agents[122] = run
	h.agentMu.Unlock()

	const n = 32
	var wins int32
	var mu sync.Mutex
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if h.agentCleanup(122, run) {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	if wins != 1 {
		t.Fatalf("agentCleanup reported %d winners, want exactly 1", wins)
	}
	if h.agentSessionFor(122) != nil {
		t.Fatal("the run stayed in the active map")
	}
}

// TestExitClosesAgentAndShell covers /exit with an agent running: it is the
// "close everything" command, and the agent holds its own PTY that the shell
// teardown cannot reach.
func TestExitClosesAgentAndShell(t *testing.T) {
	h, auditPath := testAgentHandler(t, writeAgentFixture(t))
	f := newFakeTelegram(t)
	st := selectTestProject(t, h, 130)
	h.handle(context.Background(), f.bot, 130, testAgentOwner, "agent ")
	run := h.agentSessionFor(130)
	if run == nil {
		t.Fatal("agent session not registered")
	}
	waitAgentFrame(t, run, "ready>")

	h.handleCommand(context.Background(), f.bot, 130, testAgentOwner, st, "/exit")

	if h.agentSessionFor(130) != nil {
		t.Fatal("/exit left the agent registered")
	}
	if run.sess.IsAlive() {
		t.Fatal("/exit left the agent process running")
	}
	if !f.saw("Shell closed") {
		t.Fatalf("no shell confirmation; owner saw: %v", f.messages())
	}
	// The audit trail records that /exit also stopped the agent, so the two
	// closures stay distinguishable after the fact.
	var found bool
	for _, e := range readAuditEntries(t, auditPath) {
		if e.Action == audit.ActionAgentStop && strings.Contains(e.Cmd, "/exit") {
			found = true
		}
	}
	if !found {
		t.Fatalf("/exit did not audit the agent stop: %+v", readAuditEntries(t, auditPath))
	}
}

// writeTeardownAgentFixture writes an agent fixture that behaves like a real TUI
// on the way out: on SIGINT it clears the screen and prints a goodbye banner.
// A shell that exits quietly cannot show that the delivered screen is the session
// and not the shutdown, because for it the two are the same thing.
func writeTeardownAgentFixture(t *testing.T) string {
	t.Helper()
	script := `#!/bin/sh
printf 'ready> '
trap 'printf "\033[2J\033[Hgoodbye\n"; exit 0' INT
while read -r line; do
	printf 'echo:%s\n' "$line"
done
`
	path := filepath.Join(t.TempDir(), "teardown-agent.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestExitScreenIsReadBeforeTheTUIShutsDown is the regression for the close that
// reported a couple of stray words instead of the session.
//
// The fixture answers SIGINT the way a real TUI does: it clears the screen and
// prints a goodbye banner. That is what makes the ordering observable — a screen
// read after Close is a transcript of the shutdown, and a screen read before it
// is the session. The /agent stop path used to read afterwards, so it reported
// the banner and whatever the TUI last drew over the conversation.
//
// The assertions are about the delivered message, not about the live Session, so
// that the post-shutdown state cannot be mistaken for the answer.
func TestExitScreenIsReadBeforeTheTUIShutsDown(t *testing.T) {
	h, _ := testAgentHandler(t, writeTeardownAgentFixture(t))
	f := newFakeTelegram(t)
	st := selectTestProject(t, h, 231)
	h.handle(context.Background(), f.bot, 231, testAgentOwner, "agent ")
	run := h.agentSessionFor(231)
	if run == nil {
		t.Fatal("agent session not registered")
	}
	waitAgentFrame(t, run, "ready>")

	// A turn of real conversation, then close.
	h.handleCommand(context.Background(), f.bot, 231, testAgentOwner, st, "hello there")
	waitAgentFrame(t, run, "echo:hello there")

	h.handleCommand(context.Background(), f.bot, 231, testAgentOwner, st, "/agent stop")

	if !f.saw("Agent exited") {
		t.Fatalf("no exit announcement; owner saw: %v", f.messages())
	}
	// The exit notice is text now. An image would be neither copyable nor
	// searchable, and the owner asked for what was on the shell as written
	// output.
	if f.sawPhoto() || f.sawDocument() {
		t.Fatalf("exit posted an image; the last screen is text now; owner saw: %v", f.messages())
	}
	// The fixture's banner is what makes this a real teardown rather than a quiet
	// exit: without it, a screen of the shutdown and a screen of the session are
	// indistinguishable and this assertion could not fail.
	waitForScreen(t, run, "goodbye")
	if !f.saw("echo:hello there") {
		t.Fatalf("exit message lost the conversation; it was read after the TUI "+
			"redrew. owner saw: %v", f.messages())
	}
	// And it must not be the shutdown either.
	if f.saw("goodbye") {
		t.Fatalf("exit message reported the shutdown banner instead of the "+
			"session; owner saw: %v", f.messages())
	}
}

// waitForScreen blocks until the visible screen contains want, or fails. The PTY
// is drained on its own goroutine, so a teardown draw lands asynchronously and
// reading immediately after Close would race it.
func waitForScreen(t *testing.T, run *agentRun, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if strings.Contains(run.sess.Frame(), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the fixture's TUI never drew %q, so this test cannot tell "+
				"the session from the shutdown; frame=%q", want, run.sess.Frame())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestExitSendsTheAgentScreenAsText is the behaviour the owner asked for:
// closing with /exit leaves the agent's final screen behind, as written text.
func TestExitSendsTheAgentScreenAsText(t *testing.T) {
	h, _ := testAgentHandler(t, writeAgentFixture(t))
	f := newFakeTelegram(t)
	st := selectTestProject(t, h, 131)
	h.handle(context.Background(), f.bot, 131, testAgentOwner, "agent ")
	run := h.agentSessionFor(131)
	if run == nil {
		t.Fatal("agent session not registered")
	}
	waitAgentFrame(t, run, "ready>")

	h.handleCommand(context.Background(), f.bot, 131, testAgentOwner, st, "/exit")

	if !f.saw("Agent exited") {
		t.Fatalf("no exit announcement; owner saw: %v", f.messages())
	}
	// Text, not an image: the owner asked for what was on the shell to arrive as
	// written output, and a rendered PNG is neither copyable nor searchable. The
	// live relay's photo is what covers "what did it look like".
	if f.sawPhoto() || f.sawDocument() {
		t.Fatalf("the exit screen was posted as an image; owner saw: %v", f.messages())
	}
	if !f.saw("ready>") {
		t.Fatalf("the exit message does not carry the final screen; owner saw: %v", f.messages())
	}
}

// TestRelayStillSendsPhotos guards the other half of the split: the live relay
// re-uploads every frame, so it must keep using photos. A document per frame
// would drop a file card into the chat on every tick.
func TestRelayStillSendsPhotos(t *testing.T) {
	h, _ := testAgentHandler(t, writeAgentFixture(t))
	f := newFakeTelegram(t)
	selectTestProject(t, h, 140)
	h.handle(context.Background(), f.bot, 140, testAgentOwner, "agent ")
	run := h.agentSessionFor(140)
	if run == nil {
		t.Fatal("agent session not registered")
	}
	waitAgentFrame(t, run, "ready>")

	// Drive one relay frame directly instead of waiting on the ticker.
	h.renderFramePNG(run, []byte("\x89PNG\r\n\x1a\nnot-a-real-png"))

	if !f.sawPhoto() {
		t.Fatal("live relay did not send a photo")
	}
	if f.sawDocument() {
		t.Fatal("live relay sent a document; every frame would become a file card")
	}
}

// TestTypingIndicatorFollowsThePtyPump pins the "typing…" indicator to real TUI
// output. The indicator is only meant to show while the screen is actively
// redrawing, which is what the PTY pump reports: its onPkt callback stamps
// lastPkt. That callback used to be handed to agent.Start as nil, so lastPkt
// stayed at the zero time, the freshness check in maybeTyping never held, and
// the indicator was silently never sent at all. This is the regression test for
// that: a run that has produced output must be able to show typing.
func TestTypingIndicatorFollowsThePtyPump(t *testing.T) {
	h, _ := testAgentHandler(t, writeAgentFixture(t))
	f := newFakeTelegram(t)
	selectTestProject(t, h, 150)
	h.handle(context.Background(), f.bot, 150, testAgentOwner, "agent ")
	run := h.agentSessionFor(150)
	if run == nil {
		t.Fatal("agent session not registered")
	}
	waitAgentFrame(t, run, "ready>")

	run.mu.Lock()
	lastPkt := run.lastPkt
	run.mu.Unlock()
	if lastPkt.IsZero() {
		t.Fatal("the PTY pump never stamped lastPkt, so maybeTyping can never fire")
	}

	// waitAgentFrame waits for the screen, by which point the fixture has gone
	// quiet and lastPkt is legitimately stale. Re-stamp it to stand in for a TUI
	// that is redrawing right now, which is the state the indicator is for.
	run.mu.Lock()
	run.lastPkt = time.Now()
	run.mu.Unlock()

	before := f.chatActionCount()
	h.maybeTyping(run)
	if f.chatActionCount() <= before {
		t.Fatalf("the screen is redrawing but no typing action was sent; actions: %v", f.chatActions)
	}
}

// TestTypingIndicatorStaysQuietWithoutOutput is the other half: the indicator
// must not run forever on an idle session. With no packet stamped, maybeTyping
// has to decide the screen is quiet and send nothing.
func TestTypingIndicatorStaysQuietWithoutOutput(t *testing.T) {
	h, _ := testAgentHandler(t, writeAgentFixture(t))
	f := newFakeTelegram(t)
	selectTestProject(t, h, 151)
	h.handle(context.Background(), f.bot, 151, testAgentOwner, "agent ")
	run := h.agentSessionFor(151)
	if run == nil {
		t.Fatal("agent session not registered")
	}

	// Simulate a session that went silent: pretend the last packet was long ago
	// by never stamping it, which is the same state the pump would leave behind
	// after the TUI stopped writing.
	run.mu.Lock()
	run.lastPkt = time.Time{}
	run.mu.Unlock()

	h.maybeTyping(run)
	if n := f.chatActionCount(); n != 0 {
		t.Fatalf("a silent session sent %d typing actions", n)
	}
}

// TestExitWithoutAgentStaysQuiet makes sure the new teardown does not add noise
// to the ordinary case: with no agent running, /exit is one message as before.
func TestExitWithoutAgentStaysQuiet(t *testing.T) {
	h, auditPath := testAgentHandler(t, writeAgentFixture(t))
	f := newFakeTelegram(t)
	st := selectTestProject(t, h, 132)

	h.handleCommand(context.Background(), f.bot, 132, testAgentOwner, st, "/exit")

	if !f.saw("Shell closed") {
		t.Fatalf("no shell confirmation; owner saw: %v", f.messages())
	}
	if f.saw("Agent exited") {
		t.Fatalf("/exit reported an agent exit with no agent running: %v", f.messages())
	}
	for _, e := range readAuditEntries(t, auditPath) {
		if e.Action == audit.ActionAgentStop {
			t.Fatalf("/exit audited an agent stop with no agent running: %+v", e)
		}
	}
}

// TestExitDoesNotDoubleReport covers the race: if the relay already claimed the
// teardown (the agent quit on its own), /exit must not post a second copy of the
// final screen for the same closure.
func TestExitDoesNotDoubleReport(t *testing.T) {
	h, _ := testAgentHandler(t, writeAgentFixture(t))
	f := newFakeTelegram(t)
	st := selectTestProject(t, h, 133)
	h.handle(context.Background(), f.bot, 133, testAgentOwner, "agent ")
	run := h.agentSessionFor(133)
	if run == nil {
		t.Fatal("agent session not registered")
	}
	waitAgentFrame(t, run, "ready>")

	// The relay wins first, as it does when the agent process ends by itself.
	h.onAgentExit(run)
	if h.agentSessionFor(133) != nil {
		t.Fatal("onAgentExit left the run registered")
	}
	before := f.exitsPosted()

	h.handleCommand(context.Background(), f.bot, 133, testAgentOwner, st, "/exit")

	if after := f.exitsPosted(); after != before {
		t.Fatalf("agent exit reported %d times after the relay already claimed it", after-before)
	}
	if !f.saw("Shell closed") {
		t.Fatalf("/exit did not still close the shell; owner saw: %v", f.messages())
	}
}

// TestAgentCloseIsNotACommand pins the removal of the `/agent close` alias.
//
// It used to hide the scrollback hint line, which is nothing like what the name
// promises: someone typing `/agent close` reasonably expects the agent session
// to close, and instead only one line of text disappeared while the CLI kept
// running. One name per action is worth more than a second spelling, and the
// command that really closes the session is `/agent exit`.
func TestAgentCloseIsNotACommand(t *testing.T) {
	h, _ := testAgentHandler(t, writeAgentFixture(t))
	f := newFakeTelegram(t)
	st := selectTestProject(t, h, 150)
	h.handle(context.Background(), f.bot, 150, testAgentOwner, "agent ")
	run := h.agentSessionFor(150)
	if run == nil {
		t.Fatal("agent session not registered")
	}
	waitAgentFrame(t, run, "ready>")

	h.handleCommand(context.Background(), f.bot, 150, testAgentOwner, st, "/agent close")

	if !f.saw("unknown /agent subcommand") {
		t.Fatalf("/agent close was not rejected; owner saw: %v", f.messages())
	}
	// The important part: it must not have closed anything.
	if h.agentSessionFor(150) == nil {
		t.Fatal("/agent close closed the agent session after all")
	}
	if !run.sess.IsAlive() {
		t.Fatal("/agent close killed the agent process after all")
	}
}

// TestAgentHistorySendsDocument covers the reader's upload method.
//
// A page of scrollback is as wide as the TUI, so a photo of it lands at roughly
// 3px per character cell in a chat bubble. The reader must upload a document,
// which Telegram shows at native resolution, while still scrolling inside one
// message rather than flooding the chat.
func TestAgentHistorySendsDocument(t *testing.T) {
	h, _ := testAgentHandler(t, writeAgentFixture(t))
	f := newFakeTelegram(t)
	st := selectTestProject(t, h, 151)
	h.handle(context.Background(), f.bot, 151, testAgentOwner, "agent ")
	run := h.agentSessionFor(151)
	if run == nil {
		t.Fatal("agent session not registered")
	}
	waitAgentFrame(t, run, "ready>")

	// Draw enough lines for the view to span more than one page.
	for i := 0; i < agentHistoryPage+10; i++ {
		h.handleAgentMessage(context.Background(), f.bot, 151, testAgentOwner, run, fmt.Sprintf("line%d", i))
		waitAgentFrame(t, run, fmt.Sprintf("echo:line%d", i))
	}

	h.handleCommand(context.Background(), f.bot, 151, testAgentOwner, st, "/agent history")

	if !f.sawDocument() {
		t.Fatal("the reader did not send a document")
	}
	// The live relay is still running and uploads agent.png of its own, so match
	// on the reader's own filename rather than counting photos.
	if f.sawPhotoNamed("agent-history.png") {
		t.Fatal("the reader sent a photo, which gets scaled to unreadable")
	}
	docs := f.uploadedDocs()
	last := docs[len(docs)-1]
	if !strings.HasSuffix(last.filename, ".png") {
		t.Errorf("reader document filename = %q, want a .png", last.filename)
	}
	if !bytes.HasPrefix(last.head, []byte("\x89PNG\r\n\x1a\n")) {
		t.Errorf("reader document does not start with PNG magic: %x", last.head)
	}
	if last.disableTypeDetection == "true" {
		t.Error("content type detection is disabled, so the PNG will not preview")
	}

	// The exit path is text now, so this document is the only place the owner
	// reads the conversation as a picture. Assert it decodes to a full page of
	// drawn rows rather than to a blank or cropped image. pngInk keeps only the
	// ink/no-ink bit per cell, so this cannot compare against the transcript's
	// wording — what it can hold is how much of the page the renderer filled, and
	// that is the failure this guards: a trimmed render of a sparse screen
	// arrives magnified and unreadable.
	ink, err := last.pngInk()
	if err != nil {
		t.Fatalf("reader document is not a decodable image: %v", err)
	}
	inked := 0
	for _, row := range strings.Split(ink, "\n") {
		if strings.Contains(row, "#") {
			inked++
		}
	}
	// The fixture fed more lines than a page holds, so a correct reader inks a
	// whole page; a blank or half-drawn image cannot reach it.
	if inked < agentHistoryPage {
		t.Fatalf("the reader inked %d rows, want a full page of %d; ink:\n%s",
			inked, agentHistoryPage, ink)
	}

	// Scrolling must repaint that same message with another document, not post a
	// new one and not downgrade back to a photo.
	editsBefore := len(f.mediaEditTypes())
	h.handleCommand(context.Background(), f.bot, 151, testAgentOwner, st, "/agent up")
	edits := f.mediaEditTypes()[editsBefore:]
	if len(edits) == 0 {
		t.Fatal("/agent up did not repaint the existing message")
	}
	for _, kind := range edits {
		if kind != "document" {
			t.Fatalf("scroll repainted with media type %q, want document", kind)
		}
	}
}

// TestStatusShowsAgentSession covers the Agent block /status adds on top of the
// project, working directory and shell tail. It is the one place a running
// agent is visible without going through /agent status, and it was previously
// undocumented and untested.
func TestStatusShowsAgentSession(t *testing.T) {
	h, _ := testAgentHandler(t, writeAgentFixture(t))
	f := newFakeTelegram(t)
	st := selectTestProject(t, h, 160)
	h.handle(context.Background(), f.bot, 160, testAgentOwner, "agent ")
	run := h.agentSessionFor(160)
	if run == nil {
		t.Fatal("agent session not registered")
	}
	waitAgentFrame(t, run, "ready>")

	out := h.formatSessionStatus(st)
	if !strings.Contains(out, "Agent:") {
		t.Fatalf("status does not mention the agent:\n%s", out)
	}
	// It must identify the run the same way /status identifies a shell: pid and
	// uptime, so the owner can tell which process is holding the project.
	if !strings.Contains(out, "pid") || !strings.Contains(out, run.project) {
		t.Fatalf("status agent block is missing pid/project:\n%s", out)
	}

	// And it goes back to the explicit "none" line once the session is gone,
	// rather than going stale or vanishing.
	h.handleCommand(context.Background(), f.bot, 160, testAgentOwner, st, "/agent exit")
	out = h.formatSessionStatus(st)
	if strings.Contains(out, "pid") {
		t.Fatalf("status still reports an agent pid after it exited:\n%s", out)
	}
	if !strings.Contains(out, "Agent: _(none)_") {
		t.Fatalf("status does not say the agent is gone:\n%s", out)
	}
}

func TestStatusWithoutAgentShowsHint(t *testing.T) {
	h, _ := testAgentHandler(t, writeAgentFixture(t))
	st := selectTestProject(t, h, 161)
	out := h.formatSessionStatus(st)
	if !strings.Contains(out, "Agent: _(none)_") {
		t.Fatalf("status omits the agent line entirely when no session ran:\n%s", out)
	}
	if !strings.Contains(out, "Shell: _(closed)_") {
		t.Fatalf("agent hint and shell hint are not symmetric:\n%s", out)
	}
}

func TestStatusHidesAgentHintWhenFeatureDisabled(t *testing.T) {
	dir := t.TempDir()
	auditPath := filepath.Join(t.TempDir(), "audit.log")
	l, err := audit.Open(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(Options{
		Authorizer: security.New(testAgentOwner, []int64{testAgentOwner}),
		Sessions:   session.NewManager(),
		Projects:   map[string]config.ProjectConfig{"app": {Path: dir}},
		Agent:      config.AgentConfig{Enabled: false},
		Audit:      l,
	})
	t.Cleanup(func() {
		h.Close()
		_ = l.Close()
	})

	st := selectTestProject(t, h, 162)
	if out := h.formatSessionStatus(st); strings.Contains(out, "agent <prompt>") {
		t.Fatalf("status suggests a command the gateway cannot run:\n%s", out)
	}
}

// TestExitResetsContext covers the part of /exit that is easy to get wrong.
// closeShellFor only deletes the PTY from h.shells; the *session.State survives
// it, so an /exit that stopped there would close every process and change
// nothing the owner can see — the next plain message would reopen a shell in the
// same directory under the same project, and the command would look like a no-op.
// /exit has to clear that state, or its name is a lie.
func TestExitResetsContext(t *testing.T) {
	h, _ := testAgentHandler(t, writeAgentFixture(t))
	f := newFakeTelegram(t)
	st := selectTestProject(t, h, 163)
	if st.Project != "app" {
		t.Fatalf("test setup: project = %q, want app", st.Project)
	}
	// LastCmd is set directly rather than by running a command: testAgentHandler
	// wires no terminal runner, so a command would fail and never record itself.
	// What is under test here is that /exit clears the state, not that commands
	// populate it.
	st.LastCmd = "git status"
	h.handle(context.Background(), f.bot, 163, testAgentOwner, "agent ")
	run := h.agentSessionFor(163)
	if run == nil {
		t.Fatal("agent session not registered")
	}
	waitAgentFrame(t, run, "ready>")

	h.handleCommand(context.Background(), f.bot, 163, testAgentOwner, st, "/exit")

	if st.Project != "" {
		t.Errorf("/exit kept the project binding: %q", st.Project)
	}
	if st.LastCmd != "" {
		t.Errorf("/exit kept the last command: %q", st.LastCmd)
	}
	want, err := os.UserHomeDir()
	if err != nil {
		want = "$HOME"
	}
	if st.Cwd != want {
		t.Errorf("/exit left cwd = %q, want home %q", st.Cwd, want)
	}
	if h.agentSessionFor(163) != nil {
		t.Error("/exit left the agent registered")
	}
	if run.sess.IsAlive() {
		t.Error("/exit left the agent process running")
	}
	if !f.saw("Shell closed") {
		t.Errorf("no shell confirmation; owner saw: %v", f.messages())
	}
}

// TestCommandAfterExitStartsUnbound is the other half of the contract: the next
// plain message must come back up in the home directory with no project in the
// environment, not silently resume where /exit left off.
func TestCommandAfterExitStartsUnbound(t *testing.T) {
	// This one drives a real shell, so it needs a real runner — testAgentHandler
	// only wires an agent, which is why the exit tests can close a shell that was
	// never opened.
	h := NewHandler(Options{
		Runner:     terminal.NewRunner("/bin/zsh", 1<<20),
		Projects:   map[string]config.ProjectConfig{"app": {Path: t.TempDir()}},
		Authorizer: security.New(testAgentOwner, []int64{testAgentOwner}),
	})
	f := newFakeTelegram(t)
	st := h.sessions.Ensure("164")
	st.Project = "app"
	st.Cwd = "/tmp"

	h.handleCommand(context.Background(), f.bot, 164, testAgentOwner, st, "/exit")

	// One command proves both halves at once: the shell comes back in the home
	// directory, and with no TERMILINK_PROJECT — which is how a project session
	// is visible to the workload, so its absence is what shows the binding really
	// was dropped rather than merely forgotten in the status text.
	h.handle(context.Background(), f.bot, 164, testAgentOwner,
		`echo "[cwd=$PWD][project=$TERMILINK_PROJECT]"`)
	waitSaw(t, f, "[project=]")
}

// TestExitResetSurvivesRestart is what makes /exit a reset rather than a
// momentary effect: the cleared project, working directory and last command have
// to reach the state file, or a gateway restart would quietly bring them all
// back. Reloading from the same path is the only honest way to test that —
// reading the live *State again would just be reading the struct /exit mutated.
func TestExitResetSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "sessions.json")
	auditPath := filepath.Join(dir, "audit.log")
	l, err := audit.Open(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	f := newFakeTelegram(t)
	m := session.NewManagerWithStateFile(statePath)
	h := NewHandler(Options{
		Authorizer: security.New(testAgentOwner, []int64{testAgentOwner}),
		Sessions:   m,
		Projects:   map[string]config.ProjectConfig{"app": {Path: dir}},
		Agent:      config.AgentConfig{Enabled: true, Command: writeAgentFixture(t)},
		Audit:      l,
	})
	t.Cleanup(func() {
		h.Close()
		_ = l.Close()
	})

	st := selectTestProject(t, h, 165)
	if st.Project != "app" {
		t.Fatalf("test setup: project = %q, want app", st.Project)
	}
	h.handle(context.Background(), f.bot, 165, testAgentOwner, "git status")
	h.handleCommand(context.Background(), f.bot, 165, testAgentOwner, st, "/exit")

	reloaded, ok := session.NewManagerWithStateFile(statePath).Get("165")
	if !ok {
		t.Fatal("session missing from the state file after /exit")
	}
	if reloaded.Project != "" {
		t.Errorf("project binding came back after a restart: %q", reloaded.Project)
	}
	if reloaded.LastCmd != "" {
		t.Errorf("last command came back after a restart: %q", reloaded.LastCmd)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "$HOME"
	}
	if reloaded.Cwd != home {
		t.Errorf("cwd came back as %q after a restart, want home %q", reloaded.Cwd, home)
	}
}
