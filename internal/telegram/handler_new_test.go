package telegram

import (
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

// NewHandler is where every zero value in Options is turned into a usable
// default. A mistake here is not loud: the handler still starts, and the owner
// just sees subtly wrong behaviour, so each fallback is pinned below.

// newBareHandler builds a handler from empty Options with the home directory
// redirected, because a nil Sessions makes NewHandler fall back to
// session.NewManager(), which resolves the real ~/.termilink.
func newBareHandler(t *testing.T, mutate func(*Options)) *Handler {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	opts := Options{}
	if mutate != nil {
		mutate(&opts)
	}
	return NewHandler(opts)
}

// A caller that forgets Logger used to be one line away from a nil dereference
// on the unauthorized-message and shell-failure paths. NewHandler substitutes
// slog.Default, so h.log is never nil.
func TestNewHandlerAlwaysHasALogger(t *testing.T) {
	h := newBareHandler(t, nil)
	if h.log == nil {
		t.Fatal("NewHandler left log nil")
	}
}

// The fallback must not swallow a logger the caller did supply.
func TestNewHandlerKeepsSuppliedLogger(t *testing.T) {
	custom := slog.New(slog.NewTextHandler(discardWriter{}, nil))
	h := newBareHandler(t, func(o *Options) { o.Logger = custom })
	if h.log != custom {
		t.Fatal("NewHandler replaced a logger the caller supplied")
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestNewHandlerMaxMsgLen(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{"zero takes the default", 0, defaultMaxMsgLen},
		// Telegram rejects anything past 4096, so a larger configured value
		// has to be pulled back rather than accepted.
		{"above the telegram limit is refused", msgMaxLength + 1, defaultMaxMsgLen},
		{"at the limit is kept", msgMaxLength, msgMaxLength},
		{"a sane value is kept", 2000, 2000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newBareHandler(t, func(o *Options) { o.MaxMsgLen = tt.in })
			if h.maxMsgLen != tt.want {
				t.Fatalf("maxMsgLen = %d, want %d", h.maxMsgLen, tt.want)
			}
		})
	}
}

func TestNewHandlerTimeout(t *testing.T) {
	if h := newBareHandler(t, nil); h.timeout != defaultCmdTimeout {
		t.Fatalf("timeout = %s, want %s", h.timeout, defaultCmdTimeout)
	}
	if h := newBareHandler(t, func(o *Options) { o.Timeout = 5 * time.Minute }); h.timeout != 5*time.Minute {
		t.Fatalf("timeout = %s, want 5m", h.timeout)
	}
}

func TestNewHandlerMaxFileBytes(t *testing.T) {
	if h := newBareHandler(t, nil); h.maxFileBytes != defaultMaxFileBytes {
		t.Fatalf("maxFileBytes = %d, want %d", h.maxFileBytes, defaultMaxFileBytes)
	}
	const oneMiB = 1 << 20
	if h := newBareHandler(t, func(o *Options) { o.MaxFileBytes = oneMiB }); h.maxFileBytes != oneMiB {
		t.Fatalf("maxFileBytes = %d, want %d", h.maxFileBytes, oneMiB)
	}
}

// A nil Sessions used to be a hard nil dereference the first time a command
// ran. The fallback manager has to be usable and confined to the temp home.
func TestNewHandlerFallsBackToSessionManager(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	h := NewHandler(Options{})
	if h.sessions == nil {
		t.Fatal("NewHandler left sessions nil")
	}
	if _, ok := h.sessions.Get("1"); ok {
		t.Fatal("a brand new chat already has a state")
	}
	if h.sessions.Ensure("1") == nil {
		t.Fatal("fallback session manager cannot create a state")
	}
	if _, ok := h.sessions.Get("1"); !ok {
		t.Fatal("state created by the fallback manager is not readable")
	}
	if _, err := filepath.Glob(filepath.Join(dir, ".termilink", "*")); err != nil {
		t.Fatal(err)
	}
}
