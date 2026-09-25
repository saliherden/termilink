package telegram

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saliherden/termilink/internal/audit"
)

const testAuditOwner int64 = 111

func readAuditEntries(t *testing.T, path string) []audit.Entry {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	var entries []audit.Entry
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var e audit.Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("bad audit line %q: %v", line, err)
		}
		entries = append(entries, e)
	}
	return entries
}

func TestEntryForRedactsSecrets(t *testing.T) {
	e := entryFor(1, 2, true, audit.ActionCommand, `curl -H "Authorization: Bearer abc123" -d "password=hunter2"`)
	if e.Cmd != `curl -H "Authorization: Bearer ***redacted***" -d "password=***redacted***"` {
		t.Fatalf("cmd not redacted: %q", e.Cmd)
	}
}

func TestAuditActionForDenial(t *testing.T) {
	if auditActionForDenial("⛔ workspace: outside") != audit.ActionVetBlocked {
		t.Fatal("vet denial must map to vet_blocked")
	}
	if auditActionForDenial("only the owner can cd") != audit.ActionAccessDenied {
		t.Fatal("non-workspace denial must map to access_denied")
	}
}

func TestAuditUnauthorizedAccessDenied(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	l, err := audit.Open(path)
	if err != nil {
		t.Fatalf("open audit: %v", err)
	}
	defer l.Close()

	h := NewHandler(Options{Audit: l})
	h.rejectUnauthorized(9999, 7, "PASSWORD=hunter2 rm -rf /")

	entries := readAuditEntries(t, path)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d: %+v", len(entries), entries)
	}
	e := entries[0]
	if e.Action != audit.ActionAccessDenied {
		t.Fatalf("action = %s, want access_denied", e.Action)
	}
	if e.UserID != 9999 || e.ChatID != 7 || e.Owner {
		t.Fatalf("bad identity fields: %+v", e)
	}
	if strings.Contains(e.Cmd, "hunter2") {
		t.Fatalf("secret leaked into audit: %q", e.Cmd)
	}
	if !strings.Contains(e.Cmd, "rm -rf") {
		t.Fatalf("command lost: %q", e.Cmd)
	}
}

func TestAuditEntryForApprovalFlow(t *testing.T) {
	e := entryFor(1, testAuditOwner, true, audit.ActionApprovalReq, "sudo whoami")
	if e.Action != audit.ActionApprovalReq || e.Owner != true || e.UserID != testAuditOwner {
		t.Fatalf("bad entry: %+v", e)
	}
}
