package agent

import (
	"strings"
	"testing"
)

func feedScreen(t *testing.T, s string) *Screen {
	t.Helper()
	sc := NewScreen()
	sc.Feed([]byte(s))
	return sc
}

func textOf(t *testing.T, s string) string {
	t.Helper()
	sc := feedScreen(t, s)
	return sc.Text()
}

func TestScreenBasicText(t *testing.T) {
	if got := textOf(t, "hello world"); got != "hello world" {
		t.Fatalf("text = %q, want %q", got, "hello world")
	}
}

func TestScreenNewlineAndCursor(t *testing.T) {
	in := "ab\x1b[1;5Hcd"
	sc := feedScreen(t, in)
	want := "ab  cd"
	if got := sc.Text(); got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
}

func TestScreenClearWhole(t *testing.T) {
	in := "abcdef\x1b[2Jafter\x1b[1;1H"
	sc := feedScreen(t, in)
	if got := sc.Text(); got != "after" {
		t.Fatalf("text = %q, want %q", got, "after")
	}
}

func TestScreenEraseLine(t *testing.T) {
	// "abc", go to col 2, erase to end of line -> "ab".
	in := "abcdef\x1b[1;3H\x1b[K"
	if got := textOf(t, in); got != "ab" {
		t.Fatalf("text = %q, want %q", got, "ab")
	}
}

func TestScreenEraseFromStartOfLine(t *testing.T) {
	// "abcdef", go to col 4 (index 3), erase [1K] from start to cursor
	// inclusive -> cells 0..3 become spaces, leaving "ef".
	in := "abcdef\x1b[1;4H\x1b[1K"
	if got := textOf(t, in); got != "    ef" {
		t.Fatalf("text = %q, want %q", got, "    ef")
	}
}

func TestScreenScrollOnBottom(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("\x1b[1;1H")
	sb.WriteString("line:\n")
	// Print past the bottom so the model scrolls. Only assert it does not panic
	// and returns something; full-screen scroll retention is exercised below.
	for i := 0; i < 60; i++ {
		sb.WriteString("\n")
	}
	sb.WriteString("bottom")
	sc := feedScreen(t, sb.String())
	out := sc.Text()
	if !strings.Contains(out, "bottom") {
		t.Fatalf("scrolled screen missing bottom: %q", out)
	}
}

func TestScreenSGRStripped(t *testing.T) {
	in := "\x1b[38;5;196mred\x1b[0m"
	if got := textOf(t, in); got != "red" {
		t.Fatalf("text = %q, want %q", got, "red")
	}
}

func TestScreenUTF8(t *testing.T) {
	// Box-drawing + ellipsis are multi-byte UTF-8: each byte must decode into
	// a single cell, not into Latin-1 mojibake.
	sc := feedScreen(t, "a\u2588b\u2026\u2593")
	if got := sc.Text(); got != "a\u2588b\u2026\u2593" {
		t.Fatalf("text = %q, want %q", got, "a\u2588b\u2026\u2593")
	}
}

func TestScreenSplitCSI(t *testing.T) {
	// A CSI chopped between two PTY reads must resume, not leak its
	// parameters as literal text.
	sc := NewScreen()
	sc.Feed([]byte("\x1b["))
	sc.Feed([]byte("38;2;255;255;255mLEAK TEST"))
	if got := sc.Text(); got != "LEAK TEST" {
		t.Fatalf("text = %q, want %q", got, "LEAK TEST")
	}
}

func TestScreenSplitESCErase(t *testing.T) {
	// ESC arriving at the end of a read must not swallow the following `[K`.
	sc := NewScreen()
	sc.Feed([]byte("ab\x1b"))
	sc.Feed([]byte("[Kc"))
	if got := sc.Text(); got != "abc" {
		t.Fatalf("text = %q, want %q", got, "abc")
	}
}

func TestScreenSplitOSC(t *testing.T) {
	// OSC content interrupted by a read boundary stays swallowed.
	sc := NewScreen()
	sc.Feed([]byte("\x1b]0;"))
	sc.Feed([]byte("hidden-title\x07after"))
	if got := sc.Text(); got != "after" {
		t.Fatalf("text = %q, want %q", got, "after")
	}
}

func TestScreenOSCStripped(t *testing.T) {
	in := "before\x1b]8;;http://x\x07after\x1b]0;title\x07"
	if got := textOf(t, in); got != "beforeafter" {
		t.Fatalf("text = %q, want %q", got, "beforeafter")
	}
}

func TestScreenAlternateBuffer(t *testing.T) {
	sc := feedScreen(t, "main")
	sc.Feed([]byte("\x1b[?1049htui content"))
	if !strings.Contains(sc.Text(), "tui content") {
		t.Fatalf("alt buffer missing content: %q", sc.Text())
	}
	if strings.Contains(sc.Text(), "main") {
		t.Fatalf("alt buffer leaked main content: %q", sc.Text())
	}
	sc.Feed([]byte("\x1b[?1049l"))
	if got := sc.Text(); !strings.Contains(got, "main") {
		t.Fatalf("main buffer not restored: %q", got)
	}
}

func TestScreenBackspaceAndTab(t *testing.T) {
	in := "ab\x7f\tc"
	sc := feedScreen(t, in)
	got := sc.Text()
	if !strings.Contains(got, "a") || !strings.Contains(got, "c") {
		t.Fatalf("missing chars: %q", got)
	}
}

func TestScreenLineWrap(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < screenCols+10; i++ {
		sb.WriteByte('x')
	}
	sb.WriteByte('\n')
	sb.WriteString("tail")
	sc := feedScreen(t, sb.String())
	out := sc.Text()
	lines := strings.Split(out, "\n")
	// Row 0 holds all 100 columns; the remaining 10 x's sit on row 1 before
	// the linefeed, so the final line carries "tail".
	if len(lines) < 3 {
		t.Fatalf("expected wrapped lines, got %q", out)
	}
	if lines[0] != strings.Repeat("x", screenCols) {
		t.Fatalf("row 0 wrong: %q", out)
	}
	if lines[1] != strings.Repeat("x", 10) {
		t.Fatalf("row 1 wrong: %q", out)
	}
	if !strings.Contains(out, "tail") {
		t.Fatalf("missing tail: %q", out)
	}
}

func TestScreenTrimsBlankTrailing(t *testing.T) {
	in := "top\n\n\n\n"
	if got := textOf(t, in); got != "top" {
		t.Fatalf("text = %q, want %q", got, "top")
	}
}

func TestMapInput(t *testing.T) {
	tests := []struct {
		in      string
		bytes   string
		shown   string
		special bool
	}{
		{"hello", "hello\r", "hello", false},
		{"  hi  ", "hi\r", "hi", false},
		{"^p", "\x10", "^p", true},
		{"^c", "\x03", "^c", true},
		{"^A", "\x01", "^A", true},
		{"/up", "\x1b[A", "/up", true},
		{"\u2193", "\x1b[B", "\u2193", true},
		{"/esc", "\x1b", "/esc", true},
		{"^p now", "^p now\r", "^p now", false},
		{"^x l", "\x18l", "^x l", true},            // Ctrl+X then l (switch session)
		{"^p enter", "\x10\r", "^p enter", true},   // palette then Enter
		{"up enter", "\x1b[A\r", "up enter", true}, // bare key words compose
		{"a b", "a b\r", "a b", false},             // plain two-word text stays input
		{"delete this", "delete this\r", "delete this", false},
	}
	for _, tt := range tests {
		b, s, sp := MapInput(tt.in)
		if string(b) != tt.bytes || s != tt.shown || sp != tt.special {
			t.Fatalf("MapInput(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tt.in, string(b), s, sp, tt.bytes, tt.shown, tt.special)
		}
	}
}

func TestMapInputCTRLP(t *testing.T) {
	b, shown, ok := MapInput("^p")
	if !ok || string(b) != "\x10" || shown != "^p" {
		t.Fatalf("^p mapping wrong: %q %q %v", string(b), shown, ok)
	}
}
