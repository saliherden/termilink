package telegram

import (
	"strings"
	"testing"
	"time"

	"github.com/saliherden/termilink/internal/terminal"
)

// The formatters below are the last thing every command passes through before
// the owner reads it, and until Session 014 not one of them had a test. A bug
// in this layer does not fail a build or a run: it quietly misreports what
// happened. TestRunCommandReportsRealExitCode already covers the exit status
// end to end; these pin the text itself.

func TestFormatRunStatusLines(t *testing.T) {
	tests := []struct {
		name     string
		res      terminal.Result
		wantOK   bool
		wantLine string
	}{
		{"zero", terminal.Result{ExitCode: 0}, true, "✅ Process exited with code 0"},
		{"one", terminal.Result{ExitCode: 1}, false, "❌ Process exited with code 1"},
		{"missing frame reads as -1", terminal.Result{ExitCode: -1}, false, "❌ Process exited with code -1"},
		{"exit 127", terminal.Result{ExitCode: 127}, false, "❌ Process exited with code 127"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := formatRun("ls", tt.res)
			if !strings.Contains(out, tt.wantLine) {
				t.Fatalf("status line = %q, want it to contain %q", out, tt.wantLine)
			}
			if tt.wantOK && strings.Contains(out, "❌") {
				t.Fatalf("successful command rendered a failure: %q", out)
			}
		})
	}
}

// A command that printed nothing must say so. Emitting an empty payload reads
// as a broken bot rather than a command with no output.
func TestFormatRunEmptyOutput(t *testing.T) {
	for _, out := range []terminal.Result{
		{ExitCode: 0},
		{ExitCode: 0, Output: []byte("   \n\t ")},
	} {
		got := formatRun("true", out)
		if !strings.Contains(got, "_(no output)_") {
			t.Fatalf("empty output not marked: %q", got)
		}
	}
}

func TestFormatRunEscapesCommand(t *testing.T) {
	got := formatRun("echo `whoami`", terminal.Result{ExitCode: 0})
	if !strings.Contains(got, "echo 'whoami'") {
		t.Fatalf("backtick in command was not escaped: %q", got)
	}
}

func TestFormatErr(t *testing.T) {
	if got, want := formatErr("command failed: boom"), "⚠️ command failed: boom"; got != want {
		t.Fatalf("formatErr = %q, want %q", got, want)
	}
}

func TestFormatProjectHome(t *testing.T) {
	got := formatProjectHome("app", "/Users/me/src", []string{"build", "test"})
	for _, want := range []string{
		"✅ Project *app* selected.",
		"`/Users/me/src`",
		"• `build`",
		"• `test`",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}

	// A project with no commands must not render an empty bullet list.
	bare := formatProjectHome("app", "/Users/me/src", nil)
	if strings.Contains(bare, "Available commands") {
		t.Fatalf("no commands given but the section rendered: %q", bare)
	}
}

func TestFormatProjectsEmpty(t *testing.T) {
	if got, want := formatProjects(nil), "_No projects configured._"; got != want {
		t.Fatalf("formatProjects(nil) = %q, want %q", got, want)
	}
	if got, want := formatProjects([]string{}), "_No projects configured._"; got != want {
		t.Fatalf("formatProjects([]) = %q, want %q", got, want)
	}
}

func TestFormatProjectsList(t *testing.T) {
	got := formatProjects([]string{"app", "docs"})
	for _, want := range []string{"*Configured projects*", "• `app`", "• `docs`", "Switch with /project <name>"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}

func TestFormatSessions(t *testing.T) {
	if got, want := formatSessions(nil), "_No sessions._"; got != want {
		t.Fatalf("formatSessions(nil) = %q, want %q", got, want)
	}
	got := formatSessions([]string{"cwd: /tmp", "last: ls"})
	if !strings.Contains(got, "*Sessions*") {
		t.Fatalf("missing header in %q", got)
	}
	for _, want := range []string{"cwd: /tmp", "last: ls"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing row %q in %q", want, got)
		}
	}
}

func TestFormatStatusPlaceholders(t *testing.T) {
	got := formatStatus("", "", "")
	for _, want := range []string{noCwd, noCmd} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing placeholder %q in %q", want, got)
		}
	}
	// A project must be shown when one is bound.
	if got := formatStatus("app", "/tmp", "ls"); !strings.Contains(got, "Project: `app`") {
		t.Fatalf("bound project missing from status: %q", got)
	}
}

func TestFormatTimeout(t *testing.T) {
	got := formatTimeout("sleep 60", "still going", 90*time.Second)
	for _, want := range []string{
		"`$ sleep 60`",
		"still going",
		"⏱ Command did not finish within 1m30s.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
	// A timeout with no output must still render the output block.
	if got := formatTimeout("sleep 60", "  ", 30*time.Second); !strings.Contains(got, "_(no output)_") {
		t.Fatalf("empty timeout output not marked: %q", got)
	}
}

func TestFormatInterrupted(t *testing.T) {
	got := formatInterrupted("npm run dev", "compiling")
	for _, want := range []string{"`$ npm run dev`", "compiling", "⏹ Command interrupted."} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
	if got := formatInterrupted("npm run dev", ""); !strings.Contains(got, "_(no output)_") {
		t.Fatalf("empty interrupt output not marked: %q", got)
	}
}

// The approval prompt is the last gate in front of a dangerous command, so it
// has to state plainly that nothing has run yet.
func TestFormatApprovalPrompt(t *testing.T) {
	got := formatApprovalPrompt("rm -rf /", 90*time.Second)
	for _, want := range []string{
		"⚠️ Dangerous command detected:",
		"`rm -rf /`",
		"1m30s",
		"The command will not run until approved.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}

func TestFormatApprovalPromptEscapesBackticks(t *testing.T) {
	got := formatApprovalPrompt("rm `x`", 30*time.Second)
	if !strings.Contains(got, "rm 'x'") {
		t.Fatalf("backtick in dangerous command not escaped: %q", got)
	}
}

func TestFormatApprovalStillPending(t *testing.T) {
	got := formatApprovalStillPending("rm -rf /")
	for _, want := range []string{"⏳ Approval pending for:", "`rm -rf /`", "*yes*", "*no*"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}

func TestFormatApprovalTimeoutSaysItDidNotRun(t *testing.T) {
	got := formatApprovalTimeout()
	if !strings.Contains(got, "*not* executed") {
		t.Fatalf("expired approval does not state the command was skipped: %q", got)
	}
}

// sanitizeCodeBlock is the boundary that keeps command output from closing the
// code fence it is displayed in, and from overflowing a Telegram message.
func TestSanitizeCodeBlockStopsFenceBreakout(t *testing.T) {
	got := sanitizeCodeBlock("before ``` after")
	if strings.Contains(got, "```") {
		t.Fatalf("fence survived sanitizing: %q", got)
	}
	if !strings.Contains(got, "before ''' after") {
		t.Fatalf("fence not replaced with ''': %q", got)
	}
}

func TestSanitizeCodeBlockTrimsAndPassesEmpty(t *testing.T) {
	if got := sanitizeCodeBlock(""); got != "" {
		t.Fatalf("empty string = %q, want %q", got, "")
	}
	if got := sanitizeCodeBlock("   \n\t "); got != "" {
		t.Fatalf("whitespace = %q, want %q", got, "")
	}
}

func TestSanitizeCodeBlockTruncatesLongOutput(t *testing.T) {
	got := sanitizeCodeBlock(strings.Repeat("x", 5000))
	if strings.Contains(got, "… (truncated) …") == false {
		t.Fatalf("long output not marked as truncated: tail %q", got[len(got)-40:])
	}
	if len(got) >= 5000 {
		t.Fatalf("truncated length = %d, want < 5000", len(got))
	}
	// Anything already inside the limit must pass through untouched.
	small := strings.Repeat("y", 100)
	if got := sanitizeCodeBlock(small); got != small {
		t.Fatalf("short output was altered: %q", got)
	}
}

func TestSanitizeCodeWrapsInFence(t *testing.T) {
	got := sanitizeCode("hello")
	if !strings.HasPrefix(got, "```\n") || !strings.HasSuffix(got, "\n```") {
		t.Fatalf("sanitizeCode did not wrap in a fence: %q", got)
	}
	if strings.Contains(got[3:len(got)-3], "```") {
		t.Fatalf("inner fence survived: %q", got)
	}
}

func TestEscapeCode(t *testing.T) {
	if got, want := escapeCode("a`b`c"), "a'b'c"; got != want {
		t.Fatalf("escapeCode = %q, want %q", got, want)
	}
	if got, want := escapeCode("plain"), "plain"; got != want {
		t.Fatalf("escapeCode altered plain text: %q", got)
	}
}
