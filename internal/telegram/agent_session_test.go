package telegram

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// resetSessionProbe clears the cached "can this CLI list sessions" answer so one
// test's failing CLI does not silently disable the feature for the rest of the
// package. The cache is process-wide on purpose — see recordSessionProbe — which
// means tests have to opt back in.
func resetSessionProbe(t *testing.T) {
	t.Helper()
	sessionProbe.mu.Lock()
	saved := sessionProbe.state
	sessionProbe.state = sessionProbeState{}
	sessionProbe.mu.Unlock()
	t.Cleanup(func() {
		sessionProbe.mu.Lock()
		defer sessionProbe.mu.Unlock()
		sessionProbe.state = saved
	})
}

// writeSessionFixture creates an agent that also answers a session listing.
//
// FAKE_SESSIONS is a semicolon-separated list of `id:ageMs[:dir]` specs; each
// becomes a row stamped `now - ageMs` milliseconds, so a test can place a
// session inside or outside a run's lifetime without hardcoding a wall clock.
// A spec's directory defaults to the working directory, and overriding it is how
// a row belonging to another project is staged.
func writeSessionFixture(t *testing.T) string {
	t.Helper()
	script := `#!/bin/sh
if [ "$1" = "session" ]; then
	now=$(date +%s)000
	dir=$(pwd)
	printf '['
	first=1
	IFS=';'
	for spec in $FAKE_SESSIONS; do
		[ -z "$spec" ] && continue
		[ $first -eq 1 ] || printf ','
		first=0
		id=${spec%%:*}
		case ${spec#*:} in
			*:*) age=${spec#*:}; age=${age%%:*}; dir=${spec##*:} ;;
			*)   age=${spec#*:} ;;
		esac
		printf '{"id":"%s","created":%s,"updated":%s,"directory":"%s"}' \
			"$id" "$((now - age))" "$now" "$dir"
	done
	printf ']'
	exit 0
fi
printf 'ready> '
while read -r line; do
	printf 'echo:%s\n' "$line"
done
`
	path := filepath.Join(t.TempDir(), "session-agent.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeNoSessionFixture creates an agent whose CLI does not understand
// `session list`, the way claude and codex answer it.
func writeNoSessionFixture(t *testing.T) string {
	t.Helper()
	script := `#!/bin/sh
if [ "$1" = "session" ]; then
	printf 'unknown command\n' >&2
	exit 1
fi
printf 'ready> '
while read -r line; do
	printf 'echo:%s\n' "$line"
done
`
	path := filepath.Join(t.TempDir(), "no-session-agent.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestResolveAgentSessionIDReportsASingleCandidate is the happy path: exactly one
// session was created inside the run's lifetime, so that is ours and we say so.
func TestResolveAgentSessionIDReportsASingleCandidate(t *testing.T) {
	resetSessionProbe(t)
	bin := writeSessionFixture(t)
	dir := t.TempDir()
	t.Setenv("FAKE_SESSIONS", "ses_only:0")

	if got := resolveAgentSessionID(bin, dir, time.Now().UnixMilli()); got != "ses_only" {
		t.Fatalf("session id = %q, want ses_only", got)
	}
}

// TestResolveAgentSessionIDDeclinesWhenAmbiguous is the guard against a wrong id.
//
// Two sessions were created in the window, so there is no way to tell which one
// this run was. Reporting the newer one would look authoritative and would send
// the owner to the wrong conversation, which is worse than reporting nothing.
func TestResolveAgentSessionIDDeclinesWhenAmbiguous(t *testing.T) {
	resetSessionProbe(t)
	bin := writeSessionFixture(t)
	dir := t.TempDir()
	t.Setenv("FAKE_SESSIONS", "ses_first:1000;ses_second:0")

	if got := resolveAgentSessionID(bin, dir, time.Now().UnixMilli()); got != "" {
		t.Fatalf("session id = %q, want none when two candidates exist", got)
	}
}

// TestResolveAgentSessionIDIgnoresOlderAndForeignSessions checks the two ways the
// candidate set narrows to exactly one: sessions from before this run, and
// sessions belonging to a different directory.
func TestResolveAgentSessionIDIgnoresOlderAndForeignSessions(t *testing.T) {
	resetSessionProbe(t)
	bin := writeSessionFixture(t)
	dir := t.TempDir()
	other := t.TempDir()
	// The only row belongs to another project, so it is not ours even though it
	// is recent.
	t.Setenv("FAKE_SESSIONS", "ses_foreign:0:"+other)
	if got := resolveAgentSessionID(bin, dir, time.Now().UnixMilli()); got != "" {
		t.Fatalf("session id = %q, want none: the only candidate is another project's", got)
	}

	// Now one of our own alongside the foreign row and a much older session. Only
	// ours is both local and inside the window, so it is the answer.
	t.Setenv("FAKE_SESSIONS", "ses_mine:0;ses_ancient:3600000;ses_foreign:0:"+other)
	if got := resolveAgentSessionID(bin, dir, time.Now().UnixMilli()); got != "ses_mine" {
		t.Fatalf("session id = %q, want ses_mine", got)
	}
}

// TestResolveAgentSessionIDSkipsCLIsWithoutSessionList covers a CLI that cannot
// answer at all: no id, and the answer is cached so the next close does not pay
// for another doomed subprocess.
func TestResolveAgentSessionIDSkipsCLIsWithoutSessionList(t *testing.T) {
	resetSessionProbe(t)
	bin := writeNoSessionFixture(t)
	dir := t.TempDir()

	if got := resolveAgentSessionID(bin, dir, time.Now().UnixMilli()); got != "" {
		t.Fatalf("session id = %q, want none for a CLI with no session list", got)
	}
	if canProbeSessions() {
		t.Fatal("a failed probe was not cached, so every close pays a doomed subprocess")
	}
}

// TestResolveAgentSessionIDRejectsNonJSON guards the parse: a CLI that answers
// the subcommand with something that is not a session list is treated as having
// no sessions, not as having one whose fields are all zero.
func TestResolveAgentSessionIDRejectsNonJSON(t *testing.T) {
	resetSessionProbe(t)
	bin := filepath.Join(t.TempDir(), "chatty.sh")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf 'hello\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := resolveAgentSessionID(bin, t.TempDir(), time.Now().UnixMilli()); got != "" {
		t.Fatalf("session id = %q, want none for a non-JSON answer", got)
	}
}

// TestExitFollowsTheScreenWithTheSessionID is the end-to-end shape: the screen
// text goes out first, and the id follows it as its own message.
//
// They are two messages on purpose. The screen is what the owner asked for, so
// it must not wait on a subprocess; the id is a convenience that can afford to
// arrive late, or not at all.
func TestExitFollowsTheScreenWithTheSessionID(t *testing.T) {
	resetSessionProbe(t)
	bin := writeSessionFixture(t)
	t.Setenv("FAKE_SESSIONS", "ses_endtoend:0")

	h, auditPath := testAgentHandler(t, bin)
	f := newFakeTelegram(t)
	st := selectTestProject(t, h, 171)
	h.handle(context.Background(), f.bot, 171, testAgentOwner, "agent ")
	run := h.agentSessionFor(171)
	if run == nil {
		t.Fatal("agent session not registered")
	}
	waitAgentFrame(t, run, "ready>")

	h.handleCommand(context.Background(), f.bot, 171, testAgentOwner, st, "/agent stop")

	waitForMessage(t, f, "Agent exited")
	waitForMessage(t, f, "ses_endtoend")
	// The id is only useful with the command that reopens it, and the owner
	// cannot know it from the id alone.
	if !f.saw("opencode -s ses_endtoend") {
		t.Fatalf("the session note does not say how to resume; owner saw: %v", f.messages())
	}
	// The screen text is still text.
	if f.sawPhoto() || f.sawDocument() {
		t.Fatalf("exit posted an image; owner saw: %v", f.messages())
	}
	// And the id is traceable after the fact.
	if !auditMentions(auditPath, "agent_session", "ses_endtoend") {
		t.Fatal("the reported session id is not in the audit log")
	}
}

// TestExitSurvivesACliWithNoSessionList is the same close against an agent whose
// CLI cannot list sessions. The screen must still arrive — the id is optional,
// the output is not.
func TestExitSurvivesACliWithNoSessionList(t *testing.T) {
	resetSessionProbe(t)
	h, _ := testAgentHandler(t, writeNoSessionFixture(t))
	f := newFakeTelegram(t)
	st := selectTestProject(t, h, 172)
	h.handle(context.Background(), f.bot, 172, testAgentOwner, "agent ")
	run := h.agentSessionFor(172)
	if run == nil {
		t.Fatal("agent session not registered")
	}
	waitAgentFrame(t, run, "ready>")

	h.handleCommand(context.Background(), f.bot, 172, testAgentOwner, st, "/agent stop")

	waitForMessage(t, f, "Agent exited")
	if !f.saw("ready>") {
		t.Fatalf("the exit screen is missing for a CLI with no session list; owner saw: %v", f.messages())
	}
	// Give the lookup the same chance to report as it had in the other test, so
	// that "no id" here means it was declined rather than merely late.
	time.Sleep(200 * time.Millisecond)
	if f.saw("Session `") {
		t.Fatalf("an id was reported for a CLI that has no session list; owner saw: %v", f.messages())
	}
}

// waitForMessage polls the fake until a message contains substr, so a test can
// assert on something delivered from a goroutine — the session id lookup runs
// after the exit text, deliberately off the close path.
func waitForMessage(t *testing.T, f *fakeTelegram, substr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if f.saw(substr) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no message containing %q arrived; owner saw: %v", substr, f.messages())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// auditMentions reports whether the audit log holds an entry for action whose
// Cmd contains substr.
func auditMentions(path, action, substr string) bool {
	body, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(body), "\n") {
		if strings.Contains(line, `"action":"`+action+`"`) && strings.Contains(line, substr) {
			return true
		}
	}
	return false
}

// TestResolveAgentSessionIDNeedsBothBinAndDir covers the degenerate inputs, so a
// half-configured run reports no id instead of shelling out with a blank command.
func TestResolveAgentSessionIDNeedsBothBinAndDir(t *testing.T) {
	resetSessionProbe(t)
	bin := writeSessionFixture(t)
	t.Setenv("FAKE_SESSIONS", "ses_x:0")
	now := time.Now().UnixMilli()

	for _, tc := range []struct{ name, bin, dir string }{
		{"no binary", "", t.TempDir()},
		{"no directory", bin, ""},
		{"neither", "", ""},
	} {
		if got := resolveAgentSessionID(tc.bin, tc.dir, now); got != "" {
			t.Fatalf("%s: session id = %q, want none", tc.name, got)
		}
	}
	// A failed lookup must not poison the cache for a run that is configured
	// properly, or the next real agent would silently report no id.
	if !canProbeSessions() {
		t.Fatal("a blank configuration was cached as an unsupported CLI")
	}
}

// TestFormatAgentSessionIDQuotesTheCommand keeps the resume command copyable.
// The id and the flag are wrapped in backticks so Telegram renders them as code
// and a stray underscore cannot turn them into italics.
func TestFormatAgentSessionIDQuotesTheCommand(t *testing.T) {
	note := formatAgentSessionID("ses_abc123")
	if !strings.Contains(note, "`ses_abc123`") {
		t.Fatalf("the id is not in code quotes, so it may render as italics: %q", note)
	}
	if !strings.Contains(note, "opencode -s") {
		t.Fatalf("the note does not name the resume command: %q", note)
	}
}
