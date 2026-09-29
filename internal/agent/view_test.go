package agent

import "testing"

// ViewCount and ViewRows back /agent history, which is how the owner reads the
// scrollback of a TUI they cannot see. The clamping below is what makes "up"
// reach the oldest lines and "down" stop at the newest, so it is worth pinning
// even though the underlying screen is already well covered.

func rowTexts(rows []Row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Text
	}
	return out
}

// viewInput is long enough that viewRows keeps at least 3 usable lines:
// blank filler is dropped, so a short feed can leave fewer than the tests below
// ask for.
const viewInput = "alpha\nbravo\ncharlie\ndelta\n"

func newViewSession(t *testing.T, input string) (*Session, []Row) {
	t.Helper()
	s := &Session{scrn: feedScreen(t, input)}
	all := s.scrn.viewRows()
	if len(all) < 3 {
		t.Fatalf("test setup produced only %d view rows, need 3", len(all))
	}
	return s, all
}

func TestViewCountMatchesScreen(t *testing.T) {
	s, all := newViewSession(t, viewInput)
	if got, want := s.ViewCount(), len(all); got != want {
		t.Fatalf("ViewCount = %d, want %d", got, want)
	}
}

func TestViewRowsZeroOrNegativeNIsNil(t *testing.T) {
	s, _ := newViewSession(t, viewInput)
	if got := s.ViewRows(s.ViewCount(), 0); got != nil {
		t.Fatalf("n=0 returned %v, want nil", got)
	}
	if got := s.ViewRows(s.ViewCount(), -3); got != nil {
		t.Fatalf("n<0 returned %v, want nil", got)
	}
}

// The reader starts at the end, so ViewRows(ViewCount) must be the newest rows.
func TestViewRowsAtEndReturnsNewest(t *testing.T) {
	s, all := newViewSession(t, viewInput)
	n := 2
	got := rowTexts(s.ViewRows(s.ViewCount(), n))
	want := rowTexts(all[len(all)-n:])
	if len(got) != len(want) {
		t.Fatalf("ViewRows returned %d rows, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestViewRowsClampsEndAboveView(t *testing.T) {
	s, _ := newViewSession(t, viewInput)
	// A caller passing a stale or oversized end must get the newest rows
	// rather than an empty slice or a panic.
	if got, want := len(s.ViewRows(10_000, 3)), 3; got != want {
		t.Fatalf("rows above view = %d, want %d", got, want)
	}
}

// end is an exclusive index, so a negative one clamps to "before the first
// line" and yields nothing. What matters is that nonsense input is rejected
// quietly instead of slicing out of range.
func TestViewRowsClampsNegativeEndToEmpty(t *testing.T) {
	s, _ := newViewSession(t, viewInput)
	if got := s.ViewRows(-10, 2); len(got) != 0 {
		t.Fatalf("negative end returned %d rows, want 0", len(got))
	}
}

// Asking for more lines than exist returns the whole view rather than padding.
func TestViewRowsNLongerThanViewReturnsWholeView(t *testing.T) {
	s, all := newViewSession(t, viewInput)
	got := s.ViewRows(s.ViewCount(), len(all)+50)
	if len(got) != len(all) {
		t.Fatalf("rows = %d, want %d", len(got), len(all))
	}
}
