package agent

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func TestPersistScrollRecordsLeavingLines(t *testing.T) {
	sc := NewScreen()
	persist := func() {
		sc.mu.Lock()
		sc.persistScroll()
		sc.mu.Unlock()
	}
	count := func() int {
		sc.mu.Lock()
		defer sc.mu.Unlock()
		return len(sc.history)
	}

	// Frame 1: two distinct lines on screen.
	sc.Feed([]byte("first line here\r\nsecond line here"))
	persist() // establishes lastFrame; nothing should be recorded yet
	if got := count(); got != 0 {
		t.Fatalf("history = %d lines, want 0 before anything scrolled", got)
	}

	// Frame 2: both lines are replaced, so both must be captured.
	sc.Feed([]byte("\x1b[2J\x1b[Hthird line here"))
	persist()

	want := []string{"first line here", "second line here"}
	sc.mu.Lock()
	got := historyTexts(sc.history)
	sc.mu.Unlock()
	if len(got) != len(want) {
		t.Fatalf("history = %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("history[%d] = %q, want %q", i, got[i], w)
		}
	}
}

func TestPersistScrollNoDuplicates(t *testing.T) {
	sc := NewScreen()
	persist := func() {
		sc.mu.Lock()
		sc.persistScroll()
		sc.mu.Unlock()
	}

	sc.Feed([]byte("stable line text"))
	persist()

	// A repaint replaces it, then paints the same content back twice; the
	// original line must be recorded exactly once.
	sc.Feed([]byte("\x1b[2J\x1b[Hnew line text"))
	persist()
	sc.Feed([]byte("\x1b[2J\x1b[Hstable line text"))
	persist()
	persist()

	sc.mu.Lock()
	got := historyTexts(sc.history)
	sc.mu.Unlock()

	// Both lines genuinely left the screen, so both belong in the transcript —
	// but each exactly once despite the repaint.
	if len(got) != 2 {
		t.Fatalf("history = %v, want the two replaced lines", got)
	}
	occurrences := 0
	for _, l := range got {
		if l == "stable line text" {
			occurrences++
		}
	}
	if occurrences != 1 {
		t.Fatalf("stable line recorded %d times, want 1 (history=%v)", occurrences, got)
	}
}

func TestPersistScrollSkipsShortLines(t *testing.T) {
	sc := NewScreen()
	persist := func() {
		sc.mu.Lock()
		sc.persistScroll()
		sc.mu.Unlock()
	}
	sc.Feed([]byte("ab"))
	persist()
	sc.Feed([]byte("\x1b[2J\x1b[H"))
	persist()
	sc.mu.Lock()
	got := len(sc.history)
	sc.mu.Unlock()
	if got != 0 {
		t.Fatalf("history has %d lines, want short/blank lines skipped", got)
	}
}

func TestPersistScrollCapsHistory(t *testing.T) {
	sc := NewScreen()
	persist := func() {
		sc.mu.Lock()
		sc.persistScroll()
		sc.mu.Unlock()
	}
	for i := 0; i < historyCap+50; i++ {
		sc.Feed([]byte(fmt.Sprintf("line number %d", i)))
		persist()
		sc.Feed([]byte("\x1b[2J\x1b[H"))
		persist()
	}
	sc.mu.Lock()
	n := len(sc.history)
	first := sc.history[0].Text
	last := sc.history[n-1].Text
	sc.mu.Unlock()

	if n > historyCap {
		t.Fatalf("history = %d lines, want <= %d", n, historyCap)
	}
	if !strings.HasPrefix(last, "line number 2049") {
		t.Fatalf("last line = %q, want the newest line", last)
	}
	if !strings.HasPrefix(first, "line number 50") {
		t.Fatalf("first line = %q, want the oldest retained line", first)
	}
}

func TestViewRowsJoinsTranscriptAndScreen(t *testing.T) {
	sc := NewScreen()
	persist := func() {
		sc.mu.Lock()
		sc.persistScroll()
		sc.mu.Unlock()
	}

	// Sixty lines scroll off, then the screen holds the current tail.
	for i := 0; i < 60; i++ {
		sc.Feed([]byte(fmt.Sprintf("line%02d", i)))
		persist()
		sc.Feed([]byte("\x1b[2J\x1b[H"))
		persist()
	}
	sc.Feed([]byte("current tail one\r\ncurrent tail two"))
	persist()

	sc.mu.Lock()
	all := historyTexts(sc.viewRows())
	sc.mu.Unlock()

	// Everything the TUI showed must be present exactly once, in order, with
	// no gap between what scrolled away and what is on screen now.
	seen := make(map[string]int, len(all))
	for _, l := range all {
		seen[l]++
	}
	for i := 0; i < 60; i++ {
		want := fmt.Sprintf("line%02d", i)
		if seen[want] != 1 {
			t.Fatalf("%q appears %d times in the view, want 1", want, seen[want])
		}
	}
	if seen["current tail one"] != 1 || seen["current tail two"] != 1 {
		t.Fatalf("live screen rows missing from the view: %v", all[len(all)-3:])
	}
	if len(all) != 62 {
		t.Fatalf("view has %d lines, want 62 (60 transcript + 2 live)", len(all))
	}
}

func TestViewRowsDedupsRedrawnLine(t *testing.T) {
	sc := NewScreen()
	sc.Feed([]byte("status line here"))
	sc.mu.Lock()
	sc.persistScroll()
	sc.mu.Unlock()
	// A status line redrawn in place must not appear twice in one view.
	sc.Feed([]byte("\x1b[2J\x1b[Hstatus line here"))
	sc.mu.Lock()
	sc.persistScroll()
	all := historyTexts(sc.viewRows())
	sc.mu.Unlock()

	count := 0
	for _, l := range all {
		if l == "status line here" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("redrawn status line appears %d times, want 1: %v", count, all)
	}
}

func TestRenderRowsPNG(t *testing.T) {
	rows := []Row{
		{Text: "hello history", Cells: plainCells("hello history")},
		{Text: "second line", Cells: plainCells("second line")},
		{Text: "third line", Cells: plainCells("third line")},
	}
	img, err := RenderRowsPNG(rows, " AGENT SCROLLBACK · 1–3 of 3", "/agent up · /agent off")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(img, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatal("not a PNG")
	}
	dec, err := png.Decode(bytes.NewReader(img))
	if err != nil {
		t.Fatal(err)
	}
	// Title bar + three rows + hint line.
	if dec.Bounds().Dy() != 5*renderCellH {
		t.Fatalf("height = %d, want 5 rows (title + 3 + hint)", dec.Bounds().Dy())
	}
	// The title is the longest line and sets the width.
	wantCols := utf8.RuneCountInString(" AGENT SCROLLBACK · 1–3 of 3")
	if dec.Bounds().Dx() != wantCols*renderCellW {
		t.Fatalf("width = %d, want %d cells", dec.Bounds().Dx(), wantCols)
	}
}

func TestRenderRowsPNGColorsCells(t *testing.T) {
	rows := []Row{{
		Text:  "RR",
		Cells: []RowCell{{R: 'R'}, {R: 'R', FG: colorAttr{set: true, rgb: 0xff0000}}},
	}}
	img, err := RenderRowsPNG(rows, "", "")
	if err != nil {
		t.Fatal(err)
	}
	dec, err := png.Decode(bytes.NewReader(img))
	if err != nil {
		t.Fatal(err)
	}
	// A default-colored glyph and an explicitly red one must differ, otherwise
	// the scrollback would still read as flat text. Sample across the whole
	// cell because a glyph is not solid and the cell center can be a gap.
	if pixelAt(dec, renderCellW/2, renderCellH/2) == pixelAt(dec, renderCellW+renderCellW/2, renderCellH/2) {
		t.Fatal("colored cell rendered exactly like a default one")
	}
	if !cellHasRed(dec, renderCellW, 0, renderCellW, renderCellH) {
		t.Fatal("no red-dominant pixel inside the explicitly red cell")
	}
	if cellHasRed(dec, 0, 0, renderCellW, renderCellH) {
		t.Fatal("default-colored cell painted red")
	}
}

func TestRenderRowsPNGNoChrome(t *testing.T) {
	img, err := RenderRowsPNG([]Row{{Text: "only row", Cells: plainCells("only row")}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	dec, err := png.Decode(bytes.NewReader(img))
	if err != nil {
		t.Fatal(err)
	}
	if dec.Bounds().Dy() != renderCellH {
		t.Fatalf("height = %d, want 1 row with no title or hint", dec.Bounds().Dy())
	}
}

func TestRenderRowsPNGEmpty(t *testing.T) {
	img, err := RenderRowsPNG(nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	dec, err := png.Decode(bytes.NewReader(img))
	if err != nil {
		t.Fatal(err)
	}
	if dec.Bounds().Dx() < renderCellW || dec.Bounds().Dy() < renderCellH {
		t.Fatalf("empty render too small: %dx%d", dec.Bounds().Dx(), dec.Bounds().Dy())
	}
}

func TestPersistScrollKeepsColors(t *testing.T) {
	sc := NewScreen()
	persist := func() {
		sc.mu.Lock()
		sc.persistScroll()
		sc.mu.Unlock()
	}
	// Truecolor foreground and background, so the assertion can use exact RGB.
	sc.Feed([]byte("\x1b[38;2;255;0;0;48;2;0;0;255mcolored line here\x1b[0m"))
	persist()
	sc.Feed([]byte("\x1b[2J\x1b[Hreplacement line"))
	persist()

	sc.mu.Lock()
	rows := append([]Row(nil), sc.history...)
	sc.mu.Unlock()

	if len(rows) != 1 {
		t.Fatalf("history has %d rows, want 1", len(rows))
	}
	row := rows[0]
	if row.Text != "colored line here" {
		t.Fatalf("Text = %q, want the plain line", row.Text)
	}
	if len(row.Cells) != utf8.RuneCountInString(row.Text) {
		t.Fatalf("Cells = %d, want one per rune of %q", len(row.Cells), row.Text)
	}
	if row.Cells[0].R != 'c' {
		t.Fatalf("Cells[0].R = %q, want 'c'", row.Cells[0].R)
	}
	if !row.Cells[0].FG.set || row.Cells[0].FG.rgb != 0xff0000 {
		t.Fatalf("Cells[0].FG = %+v, want red 0xff0000", row.Cells[0].FG)
	}
	if !row.Cells[0].BG.set || row.Cells[0].BG.rgb != 0x0000ff {
		t.Fatalf("Cells[0].BG = %+v, want blue 0x0000ff", row.Cells[0].BG)
	}
	// A line written after the reset must fall back to the terminal defaults.
	sc.Feed([]byte("\x1b[2J\x1b[Hplain default line"))
	persist()
	sc.Feed([]byte("\x1b[2J\x1b[Hreplacement again"))
	persist()
	sc.mu.Lock()
	plain := sc.history[len(sc.history)-1]
	sc.mu.Unlock()
	if plain.Text != "plain default line" {
		t.Fatalf("last row = %q, want the post-reset line", plain.Text)
	}
	if plain.Cells[0].FG.set || plain.Cells[0].BG.set {
		t.Fatalf("post-reset cell = FG %+v BG %+v, want both unset", plain.Cells[0].FG, plain.Cells[0].BG)
	}
}

func TestPersistScrollKeepsAnsiPaletteColor(t *testing.T) {
	sc := NewScreen()
	persist := func() {
		sc.mu.Lock()
		sc.persistScroll()
		sc.mu.Unlock()
	}
	sc.Feed([]byte("\x1b[31mpalette line here\x1b[0m"))
	persist()
	sc.Feed([]byte("\x1b[2J\x1b[Hreplacement line"))
	persist()

	sc.mu.Lock()
	rows := append([]Row(nil), sc.history...)
	sc.mu.Unlock()
	if len(rows) != 1 {
		t.Fatalf("history has %d rows, want 1", len(rows))
	}
	if got := rows[0].Cells[0].FG; !got.set || got.rgb != ansiPalette[1] {
		t.Fatalf("SGR 31 FG = %+v, want palette entry 0x%06x", got, ansiPalette[1])
	}
}

func TestViewRowsExposesSessionColors(t *testing.T) {
	sc := NewScreen()
	sc.Feed([]byte("\x1b[38;2;0;255;0;48;2;0;0;255mlive colored line\x1b[0m"))
	sc.mu.Lock()
	rows := historyTexts(sc.viewRows())
	cells := sc.viewRows()
	sc.mu.Unlock()

	if !contains(rows, "live colored line") {
		t.Fatalf("view rows = %v, want the live line", rows)
	}
	var found *Row
	for i := range cells {
		if cells[i].Text == "live colored line" {
			found = &cells[i]
		}
	}
	if found == nil {
		t.Fatal("live line missing from view rows")
	}
	if found.Cells[0].FG.rgb != 0x00ff00 {
		t.Fatalf("live FG = %+v, want green 0x00ff00", found.Cells[0].FG)
	}
	if found.Cells[0].BG.rgb != 0x0000ff {
		t.Fatalf("live BG = %+v, want blue 0x0000ff", found.Cells[0].BG)
	}
}

// plainCells builds default-colored cells for a plain string.
func plainCells(s string) []RowCell {
	cells := make([]RowCell, 0, utf8.RuneCountInString(s))
	for _, r := range s {
		cells = append(cells, RowCell{R: r})
	}
	return cells
}

// TestRenderConcurrentWithScreen renders the live screen and the scrollback
// reader at the same time from many goroutines.
//
// The two paths share one font.Face, and opentype.Face carries per-glyph state
// (sfnt buffer, mask image, vector rasterizer). Before renderMu existed this
// raced: the process died with "slice bounds out of range" inside
// vector.fixedLineTo whenever a `/agent history` render overlapped a relay
// frame. Under -race this test reports the corruption; without the lock it is
// not merely a data race, it is a process-killing one.
func TestRenderConcurrentWithScreen(t *testing.T) {
	sc := NewScreen()
	sc.Feed([]byte("\x1b[38;2;0;255;0mconcurrent render\x1b[0m"))

	rows := make([]Row, 8)
	for i := range rows {
		rows[i] = Row{Text: "history row", Cells: plainCells("history row")}
	}

	var wg sync.WaitGroup
	errs := make(chan error, 8*32)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 32; j++ {
				if _, err := RenderRowsPNG(rows, "AGENT SCROLLBACK", "hint"); err != nil {
					errs <- fmt.Errorf("RenderRowsPNG: %w", err)
					return
				}
				if _, err := sc.PNG(); err != nil {
					errs <- fmt.Errorf("PNG: %w", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func historyTexts(rows []Row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Text
	}
	return out
}

func contains(hay []string, want string) bool {
	for _, s := range hay {
		if s == want {
			return true
		}
	}
	return false
}

func pixelAt(img image.Image, x, y int) color.RGBA {
	r, g, b, a := img.At(x, y).RGBA()
	return color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)}
}

// cellHasRed reports whether any pixel in the cell box is clearly red, ignoring
// antialiasing against the dark background.
func cellHasRed(img image.Image, cellX, cellY, w, h int) bool {
	for y := cellY; y < cellY+h; y++ {
		for x := cellX; x < cellX+w; x++ {
			p := pixelAt(img, x, y)
			if p.R > p.G+40 && p.R > p.B+40 {
				return true
			}
		}
	}
	return false
}
