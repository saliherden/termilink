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

// writeFile creates a file holding exactly the given contents, so tests can
// seed a rotation chain with distinguishable generations.
func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("seed %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestShiftArchivesMovesGenerationsUpInOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	l, err := OpenWithOptions(path, 0, 3)
	if err != nil {
		t.Fatalf("OpenWithOptions: %v", err)
	}
	defer l.Close()

	// Stand in for a log that has already rotated twice: .1 is the newer
	// generation, .2 the older one.
	writeFile(t, path, "gen3")
	writeFile(t, path+".1", "gen2")
	writeFile(t, path+".2", "gen1")

	l.shiftArchives()

	// Each generation moves exactly one slot up. If this shifted in the wrong
	// direction, or overwrote instead of renaming, .2 would still read "gen1"
	// and the newest history would be silently lost.
	for _, tc := range []struct{ name, want string }{
		{".1", "gen3"},
		{".2", "gen2"},
		{".3", "gen1"},
	} {
		if got := readFile(t, path+tc.name); got != tc.want {
			t.Errorf("archive %s = %q, want %q", tc.name, got, tc.want)
		}
	}
	if _, err := os.Stat(path + ".4"); !os.IsNotExist(err) {
		t.Errorf("archive .4 exists with keep=3")
	}
	// The caller reopens a fresh log after the shift, so the old one must be
	// gone rather than left behind to be appended to again.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("current log still present after shift")
	}
}

func TestShiftArchivesDropsOldestWhenFull(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	l, err := OpenWithOptions(path, 0, 2)
	if err != nil {
		t.Fatalf("OpenWithOptions: %v", err)
	}
	defer l.Close()

	// A full chain: .2 is about to fall off the end.
	writeFile(t, path, "gen3")
	writeFile(t, path+".1", "gen2")
	writeFile(t, path+".2", "gen1")
	writeFile(t, path+".3", "gen0")

	l.shiftArchives()

	if got := readFile(t, path+".1"); got != "gen3" {
		t.Errorf(".1 = %q, want gen3", got)
	}
	if got := readFile(t, path+".2"); got != "gen2" {
		t.Errorf(".2 = %q, want gen2", got)
	}
	for _, name := range []string{".3", ".4"} {
		if _, err := os.Stat(path + name); !os.IsNotExist(err) {
			t.Errorf("archive %s should have been dropped (keep=2)", name)
		}
	}
}

func TestShiftArchivesToleratesGaps(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	l, err := OpenWithOptions(path, 0, 3)
	if err != nil {
		t.Fatalf("OpenWithOptions: %v", err)
	}
	defer l.Close()

	// .1 is missing, as it would be if an operator cleaned it up by hand. A
	// missing archive is not an error: the remaining one has to move up rather
	// than being left in a slot the new .1 is about to take, and the index
	// encodes age, so it lands at .3 with .2 left as a hole.
	writeFile(t, path, "gen3")
	writeFile(t, path+".2", "gen1")

	l.shiftArchives()

	if got := readFile(t, path+".1"); got != "gen3" {
		t.Errorf(".1 = %q, want gen3", got)
	}
	if got := readFile(t, path+".3"); got != "gen1" {
		t.Errorf(".3 = %q, want gen1 (the survivor must move up, not be dropped)", got)
	}
	if _, err := os.Stat(path + ".2"); !os.IsNotExist(err) {
		t.Errorf(".2 should stay a hole rather than being backfilled")
	}
}

func TestLoweringRetentionClearsLeftoverArchives(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")

	// Fill a chain the way a previous run with a higher audit_keep would have.
	full, err := OpenWithOptions(path, 0, 4)
	if err != nil {
		t.Fatalf("OpenWithOptions: %v", err)
	}
	writeFile(t, path, "gen5")
	writeFile(t, path+".1", "gen4")
	writeFile(t, path+".2", "gen3")
	writeFile(t, path+".3", "gen2")
	writeFile(t, path+".4", "gen1")
	full.shiftArchives()

	// Now the operator lowers the setting to two and the next rotation happens.
	full.keep = 2
	writeFile(t, path, "gen6")
	full.shiftArchives()

	if got := readFile(t, path+".1"); got != "gen6" {
		t.Errorf(".1 = %q, want gen6", got)
	}
	if got := readFile(t, path+".2"); got != "gen5" {
		t.Errorf(".2 = %q, want gen5", got)
	}
	// Archives past the new count must be cleaned up, not just ignored: with
	// only the ".<keep>" slot deleted they would sit on disk forever because no
	// later shift ever looks that far.
	for _, name := range []string{".3", ".4", ".5"} {
		if _, err := os.Stat(path + name); !os.IsNotExist(err) {
			t.Errorf("archive %s should have been cleared after lowering retention", name)
		}
	}
}

func TestRetentionKeepsMoreHistoryThanASingleArchive(t *testing.T) {
	// Same workload, two retention settings. Before audit_keep existed both
	// runs behaved identically, so a setting that silently did nothing would
	// still pass a test that only checked the chain existed.
	run := func(keep int) (path string, current, archived int) {
		dir := t.TempDir()
		path = filepath.Join(dir, "audit.log")
		l, err := OpenWithOptions(path, 400, keep)
		if err != nil {
			t.Fatalf("OpenWithOptions(keep=%d): %v", keep, err)
		}
		defer l.Close()
		for i := 0; i < 80; i++ {
			l.Audit(Entry{Action: ActionCommand, Cmd: fmt.Sprintf("entry-%03d", i), UserID: 1})
		}
		count := func(name string) int {
			data, err := os.ReadFile(name)
			if err != nil {
				return 0
			}
			return len(toLines(data))
		}
		for i := 1; i <= keep; i++ {
			archived += count(fmt.Sprintf("%s.%d", path, i))
		}
		return path, count(path), archived
	}

	singlePath, _, singleArchived := run(1)
	manyPath, manyCurrent, manyArchived := run(3)

	if manyArchived <= singleArchived {
		t.Errorf("keep=3 archived %d entries, keep=1 archived %d; retention is not retaining more",
			manyArchived, singleArchived)
	}
	// The current log is bounded by the cap in both runs; only the archive
	// side should differ.
	if manyCurrent > 400+1024 {
		t.Errorf("current log %d bytes far exceeds cap 400", manyCurrent)
	}
	if _, err := os.Stat(manyPath + ".4"); !os.IsNotExist(err) {
		t.Errorf("archive .4 exists with keep=3")
	}
	if _, err := os.Stat(singlePath + ".2"); !os.IsNotExist(err) {
		t.Errorf("archive .2 exists with keep=1")
	}
}

func TestKeepBelowOneIsClampedToASingleArchive(t *testing.T) {
	for _, keep := range []int{0, -1, -99} {
		dir := t.TempDir()
		path := filepath.Join(dir, "audit.log")
		l, err := OpenWithOptions(path, 300, keep)
		if err != nil {
			t.Fatalf("OpenWithOptions(keep=%d): %v", keep, err)
		}
		for i := 0; i < 30; i++ {
			l.Audit(Entry{Action: ActionCommand, Cmd: fmt.Sprintf("e-%d", i), UserID: 1})
		}
		l.Close()

		if _, err := os.Stat(path + ".1"); err != nil {
			t.Errorf("keep=%d: .1 missing after rotation: %v", keep, err)
		}
		if _, err := os.Stat(path + ".2"); !os.IsNotExist(err) {
			t.Errorf("keep=%d: .2 exists, want clamped to a single archive", keep)
		}
	}
}

func TestPathFor(t *testing.T) {
	t.Run("off disables auditing", func(t *testing.T) {
		path, enabled := PathFor("off")
		if enabled {
			t.Error("enabled = true for \"off\"")
		}
		if path != "" {
			t.Errorf("path = %q, want empty", path)
		}
	})

	t.Run("explicit path wins", func(t *testing.T) {
		path, enabled := PathFor("/var/log/termilink/audit.log")
		if !enabled {
			t.Error("enabled = false for an explicit path")
		}
		if path != "/var/log/termilink/audit.log" {
			t.Errorf("path = %q", path)
		}
	})

	t.Run("empty resolves to the default", func(t *testing.T) {
		path, enabled := PathFor("")
		if !enabled {
			t.Error("an empty value must not silently disable auditing")
		}
		if want := DefaultPath(); path != want {
			t.Errorf("path = %q, want the default %q", path, want)
		}
	})
}
