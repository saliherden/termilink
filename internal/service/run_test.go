package service

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tg "github.com/go-telegram/bot"

	"github.com/saliherden/termilink/internal/session"
)

// fakeBotAPI stands in for api.telegram.org. Only getUpdates reaches it: New
// skips getMe, and Run does nothing but poll.
//
// The pause matters. The library backs off after an *error* but not after an
// empty *success*, and its poll timeout is a full minute with no option to
// shorten it, so a server that answered instantly would leave the gateway
// re-polling in a tight loop for the whole test.
func fakeBotAPI(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		select {
		case <-time.After(250 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newRunningService builds the gateway the way main does — config in, wired
// service out — and reports the HOME it was pinned to. Run resolves the session
// state file through os.UserHomeDir, so that pinned directory is the only place
// the service can write.
func newRunningService(t *testing.T) (*Service, string) {
	t.Helper()
	cfg := testConfig(t)
	srv := fakeBotAPI(t)
	skipGetMe(t, tg.WithServerURL(srv.URL), tg.WithNotAsyncHandlers())

	svc, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return svc, os.Getenv("HOME")
}

// runUntilCancelled starts Run, gives the poller a moment to actually spin up,
// then cancels. A Run that does not come back fails the test rather than
// hanging the suite, which is the whole point: nothing has ever exercised the
// shutdown path.
func runUntilCancelled(t *testing.T, svc *Service) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()

	time.Sleep(150 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
		return nil
	}
}

// TestRunStartsAndStops is the smoke test Run never had: the gateway starts,
// polls, and shuts down cleanly. It runs with auditing off, so s.audit is nil
// and Run has to survive that.
func TestRunStartsAndStops(t *testing.T) {
	svc, _ := newRunningService(t)
	if err := runUntilCancelled(t, svc); err != nil {
		t.Fatalf("Run returned %v, want nil", err)
	}
}

// TestRunSavesSessions covers the one thing Run does that New never does: it
// persists session state. New only reads the state file, so this is the first
// execution of that write anywhere in the test suite.
func TestRunSavesSessions(t *testing.T) {
	svc, home := newRunningService(t)
	if err := runUntilCancelled(t, svc); err != nil {
		t.Fatalf("Run returned %v, want nil", err)
	}

	path := filepath.Join(home, ".termilink", "state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Run did not persist session state: %v", err)
	}
	var states []session.State
	if err := json.Unmarshal(data, &states); err != nil {
		t.Fatalf("state file is not a session list: %v (%q)", err, data)
	}
	// Nothing has opened a session yet, so the file is an empty list rather
	// than absent — an empty list is what proves Save ran.
	if len(states) != 0 {
		t.Errorf("state file holds %d sessions, want 0: %q", len(states), data)
	}
}

// TestRunConfinesWritesToTheConfiguredHome is Run's version of
// TestNewLeavesTheRealHomeUntouched. That one asserts New creates nothing; this
// asserts that the one thing Run does create is the state tree, and nothing
// else. Pinning HOME is what confines the write — it is the only input to the
// path — so the guarantee worth testing is that Run adds no stray files.
func TestRunConfinesWritesToTheConfiguredHome(t *testing.T) {
	svc, home := newRunningService(t)
	if err := runUntilCancelled(t, svc); err != nil {
		t.Fatalf("Run returned %v, want nil", err)
	}

	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != ".termilink" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("Run left %v under HOME, want only .termilink", names)
	}
}

// syncBuffer is an io.Writer safe for the poller's goroutine, which logs from
// its own thread while the test reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestRunRoutesTelegramErrorsIntoTheAppLog is the regression for an outage that
// begins after boot. A bad token is caught by tg.New's getMe call, so the only
// failures that reach here are runtime ones — the network dropping, Telegram
// returning 5xx, a token being revoked. The library retries those forever and
// reports them to its own handler, which used to be a bare log.Printf on
// stderr: invisible next to the app's logs, and a gateway that had never
// reached Telegram looked exactly like a healthy one.
func TestRunRoutesTelegramErrorsIntoTheAppLog(t *testing.T) {
	var logs syncBuffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))

	cfg := testConfig(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream is down", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	skipGetMe(t, tg.WithServerURL(srv.URL), tg.WithNotAsyncHandlers())

	svc, err := New(cfg, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()

	// The first retry waits 100ms and the backoff doubles, so a failure lands
	// within a second; allow room for a slow machine before giving up.
	deadline := time.After(10 * time.Second)
	for logs.String() == "" {
		select {
		case <-deadline:
			t.Fatal("a failing Telegram API produced no log output")
		case <-time.After(50 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}

	if got := logs.String(); !strings.Contains(got, "telegram api error") {
		t.Errorf("logs = %q, want a telegram api error line", got)
	}
}
