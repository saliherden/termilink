package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	cases := []struct{ in, want string }{
		{"curl -u foo:super-secret-url", "curl -u foo:super-secret-url"},
		{"curl -H 'Authorization: Bearer abc.def.123' -d foo", "curl -H 'Authorization: Bearer ***redacted***' -d foo"},
		{"curl -H 'Authorization: Bearer xyz'", "curl -H 'Authorization: Bearer ***redacted***'"},
		{"PASSWORD=hunter2 ls", "PASSWORD=***redacted*** ls"},
		{"export api_key=ABCD1234; npm run deploy", "export api_key=***redacted***; npm run deploy"},
		{"git push origin main", "git push origin main"},
		{"connect --auth-token=abc --user root", "connect --auth-token=***redacted*** --user root"},
		{"cp ~/secret-note.txt /tmp/", "cp ~/secret-note.txt /tmp/"},
	}
	for _, c := range cases {
		if got := Redact(c.in); got != c.want {
			t.Errorf("Redact(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestOpenAndAudit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")

	l, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()

	ok := true
	l.Audit(Entry{Action: ActionCommand, Cmd: "ls -la", UserID: 1, ChatID: 2, OK: &ok})
	l.Audit(Entry{Action: ActionCommandResult, Cmd: "ls -la", DurMS: 12, OK: Bool(true)})

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %q", len(lines), string(data))
	}
	var e Entry
	if err := json.Unmarshal([]byte(lines[0]), &e); err != nil {
		t.Fatalf("line not JSON: %v: %q", err, lines[0])
	}
	if e.Action != ActionCommand || e.Cmd != "ls -la" || e.Owner {
		t.Fatalf("unexpected entry: %+v", e)
	}

	// Second open appends, does not truncate.
	l2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	l2.Audit(Entry{Action: ActionExit, UserID: 1})
	l2.Close()

	data, _ = os.ReadFile(path)
	if got := len(strings.Split(strings.TrimSpace(string(data)), "\n")); got != 3 {
		t.Fatalf("after append got %d lines, want 3", got)
	}
}

func TestNilLoggerIsNoop(t *testing.T) {
	var l *Logger
	l.Audit(Entry{Action: ActionExit}) // must not panic
}

func TestDefaultPath(t *testing.T) {
	if p := DefaultPath(); p != "" && !strings.HasSuffix(p, ".termilink/audit.log") {
		t.Fatalf("unexpected default path: %q", p)
	}
}

func TestOpenNoDefaultHome(t *testing.T) {
	t.Setenv("HOME", "")
	// os.UserHomeDir falls back to $HOME on unix; forcing empty means default
	// resolution may still succeed via other means, so only assert the API
	// tolerates it without panicking.
	if _, err := Open(""); err == nil {
		t.Skip("default path resolved even with empty HOME")
	}
}

func TestRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")

	// Cap larger than one entry (~170B) so entries accumulate until the file
	// crosses the cap, then the whole file moves to .1 and a fresh one starts.
	l, err := OpenWithMax(path, 500)
	if err != nil {
		t.Fatalf("OpenWithMax: %v", err)
	}
	defer l.Close()

	const n = 20
	for i := 0; i < n; i++ {
		l.Audit(Entry{Action: ActionCommand, Cmd: fmt.Sprintf("rotate-%d", i), UserID: 1})
	}

	// Rotation is checked before a write, so the current file may exceed the
	// cap by at most one entry (~170B). Loosely: current+backup must stay
	// bounded and the oldest entry must have moved out.
	cur, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read current: %v", err)
	}
	if int64(len(cur)) > 500+1024 {
		t.Fatalf("current log %d bytes far exceeds cap 500", len(cur))
	}

	backup, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}

	curLines := toLines(cur)
	backupLines := toLines(backup)
	total := len(curLines) + len(backupLines)
	if total >= n || total < 2 {
		t.Fatalf("rotation kept %d of %d entries, want some but not all retained", total, n)
	}
	for name, lines := range map[string][]string{"current": curLines, "backup": backupLines} {
		for i, line := range lines {
			var e Entry
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				t.Fatalf("%s line %d not JSON: %v", name, i, err)
			}
			if !strings.HasPrefix(e.Cmd, "rotate-") {
				t.Fatalf("%s line %d cmd = %q, want rotate-N", name, i, e.Cmd)
			}
			if e.Cmd == "rotate-0" {
				t.Fatalf("%s still contains the oldest entry: %s", name, e.Cmd)
			}
		}
	}
}

func TestRotationDisabledByDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	l, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()
	for i := 0; i < 10; i++ {
		l.Audit(Entry{Action: ActionCommand, Cmd: "x"})
	}
	data, _ := os.ReadFile(path)
	if got := len(toLines(data)); got != 10 {
		t.Fatalf("got %d lines, want 10 (no rotation)", got)
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatalf("backup unexpectedly present: %v", err)
	}
}

func toLines(data []byte) []string {
	s := strings.TrimSpace(string(data))
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
