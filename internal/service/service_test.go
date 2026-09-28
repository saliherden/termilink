package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tg "github.com/go-telegram/bot"

	"github.com/saliherden/termilink/internal/config"
)

// testConfig returns a config that is valid apart from whatever the test
// changes, with every path the constructor can reach pointed inside a temp dir.
// New resolves both the session state file and the audit log through
// os.UserHomeDir(), so pinning HOME is what keeps a test run from reading or
// creating anything in the developer's real ~/.termilink.
func testConfig(t *testing.T) *config.Config {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	// os.UserHomeDir reads USERPROFILE on Windows; CI does not run there, but
	// pinning both keeps the helper honest if that ever changes.
	t.Setenv("USERPROFILE", home)

	cfg := config.Defaults()
	cfg.Telegram.BotToken = "123456789:TESTTOKEN-not-a-real-bot"
	cfg.Security.AuditLog = "off"
	return cfg
}

// skipGetMe drops the getMe call tg.New makes while constructing the client.
// Without it these tests would need the real Telegram API, and a gateway that
// cannot be built offline cannot be tested at all.
func skipGetMe(t *testing.T) {
	t.Helper()
	prev := extraBotOptions
	extraBotOptions = []tg.Option{tg.WithSkipGetMe()}
	t.Cleanup(func() { extraBotOptions = prev })
}

func TestNewWiresTheGatewayTogether(t *testing.T) {
	skipGetMe(t)
	cfg := testConfig(t)

	svc, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if svc == nil {
		t.Fatal("New returned a nil service and no error")
	}
	if svc.bot == nil {
		t.Error("no telegram bot wired")
	}
	if svc.handler == nil {
		t.Error("no handler wired")
	}
	if svc.sessions == nil {
		t.Error("no session manager wired")
	}
	// A nil logger must fall back to the default rather than panic later on,
	// which is why this passes nil.
	if got := svc.auditPath(); got != "off" {
		t.Errorf("auditPath = %q, want %q when auditing is disabled", got, "off")
	}
}

func TestNewRejectsEmptyBotToken(t *testing.T) {
	cfg := testConfig(t)
	cfg.Telegram.BotToken = "   "

	_, err := New(cfg, nil)
	if err == nil {
		t.Fatal("New accepted an empty bot token")
	}
	if !strings.Contains(err.Error(), "create telegram bot") {
		t.Errorf("error = %q, want it to mention the bot token", err)
	}
}

func TestNewRejectsUnknownApprovalMode(t *testing.T) {
	cfg := testConfig(t)
	cfg.Security.ApproveDangerous = "sometimes"

	_, err := New(cfg, nil)
	if err == nil {
		t.Fatal("New accepted an unknown approval mode")
	}
	if !strings.Contains(err.Error(), "build security policy") {
		t.Errorf("error = %q, want it to mention the security policy", err)
	}
}

func TestNewRejectsInvalidDangerousPattern(t *testing.T) {
	cfg := testConfig(t)
	// An unclosed character class is the cheapest way to get a regexp that
	// does not compile, and these patterns are the user's to write.
	cfg.Security.DangerousPatterns = []string{"rm -rf ["}

	_, err := New(cfg, nil)
	if err == nil {
		t.Fatal("New accepted a pattern that does not compile")
	}
	if !strings.Contains(err.Error(), "build security policy") {
		t.Errorf("error = %q, want it to mention the security policy", err)
	}
}

func TestNewRejectsUnwritableAuditLog(t *testing.T) {
	cfg := testConfig(t)
	// A path underneath a regular file cannot be created, and the mode is not
	// worth a platform-specific permission dance to provoke.
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.Security.AuditLog = filepath.Join(blocker, "audit.log")

	_, err := New(cfg, nil)
	if err == nil {
		t.Fatal("New accepted an audit log path it cannot open")
	}
	if !strings.Contains(err.Error(), "open audit log") {
		t.Errorf("error = %q, want it to mention the audit log", err)
	}
}

func TestNewLeavesTheRealHomeUntouched(t *testing.T) {
	skipGetMe(t)
	cfg := testConfig(t)

	if _, err := New(cfg, nil); err != nil {
		t.Fatalf("New: %v", err)
	}

	entries, err := os.ReadDir(os.Getenv("HOME"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("New created %v under HOME; it should only read", names)
	}
}

func TestNewAuditLoggerOffDisablesAuditing(t *testing.T) {
	cfg := testConfig(t)
	cfg.Security.AuditLog = "off"

	logger, err := newAuditLogger(cfg)
	if err != nil {
		t.Fatalf("newAuditLogger: %v", err)
	}
	if logger != nil {
		t.Error("auditing was disabled but a logger was built anyway")
	}
}

func TestNewAuditLoggerWritesAPrivateFile(t *testing.T) {
	cfg := testConfig(t)
	path := filepath.Join(t.TempDir(), "audit.log")
	cfg.Security.AuditLog = path

	logger, err := newAuditLogger(cfg)
	if err != nil {
		t.Fatalf("newAuditLogger: %v", err)
	}
	if logger == nil {
		t.Fatal("no logger for a configured audit path")
	}
	t.Cleanup(func() { _ = logger.Close() })

	if got := logger.Path(); got != path {
		t.Errorf("Path = %q, want %q", got, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("audit log not created: %v", err)
	}
	// The log records who ran what on the gateway, so it must not be readable
	// by anyone else on a shared machine.
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("audit log mode = %04o, want 0600", perm)
	}
}
