package agent

import (
	"strings"
	"sync"
	"unicode/utf8"
)

// Screen is a small VT-style screen model used to turn the raw ANSI byte
// stream of a full-screen TUI (like opencode) into the visible text that can
// be relayed over Telegram. It tracks a main and an alternate buffer, the
// cursor, common erase/cursor/scrolling sequences, automatic line wrap and
// SGR/OSC noise, which is enough for the sequences used by Go's TUI stack.
type Screen struct {
	mu       sync.Mutex
	main     buffer
	alt      buffer
	altOn    bool
	savedR   int
	savedC   int
	savedAlt bool
	dirty    bool
	last     rune

	// Current SGR rendition, applied to characters as they are drawn so the
	// screen can be rendered as a colored image.
	curFG colorAttr
	curBG colorAttr

	// Scrollback: distinct lines that scrolled out of the visible window,
	// captured in order with their colors so long output can be re-read via
	// /agent history.
	history   []Row
	histSeen  map[string]struct{}
	lastFrame []Row

	// ANSI parser state carried across Feed calls so that escapes split
	// between PTY reads (ESC/CSI/OSC) resume instead of leaking as text.
	pstate    int
	pparams   []int
	pprivate  bool
	poscStart int
}

// historyCap bounds the size of the scrollback transcript; oldest lines drop.
const historyCap = 2000

// cell is one terminal screen cell: a rune plus the background and foreground
// colors in effect when it was written.
type cell struct {
	r  rune
	fg colorAttr
	bg colorAttr
}

// colorAttr is an optional 24-bit color. set=false means the terminal default.
type colorAttr struct {
	set bool
	rgb uint32 // 0xRRGGBB
}

// Color is an exported 24-bit color from a captured row. Set is false when the
// terminal default was in effect.
type Color = colorAttr

// RowCell is one rune of a captured row together with the colors it was drawn
// with, so scrollback can be re-rendered exactly like the live screen.
type RowCell struct {
	R  rune
	FG Color
	BG Color
}

// Row is one captured line of TUI output: its plain text plus the colored cells
// behind it.
type Row struct {
	Text  string
	Cells []RowCell
}

type buffer struct {
	rows  [][]cell
	cursR int
	cursC int
}

const (
	screenRows = 40
	screenCols = 100
	maxRows    = 200
	maxCols    = 300
)

// NewScreen returns an empty screen sized for the agent's PTY window.
func NewScreen() *Screen {
	s := &Screen{
		main:     newBuffer(),
		alt:      newBuffer(),
		histSeen: make(map[string]struct{}),
	}
	return s
}

func newBuffer() buffer {
	rows := make([][]cell, screenRows)
	for i := range rows {
		rows[i] = make([]cell, 0, screenCols)
	}
	return buffer{rows: rows}
}

// Reset clears both buffers and returns to the main screen.
func (s *Screen) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.main = newBuffer()
	s.alt = newBuffer()
	s.altOn = false
	s.dirty = true
	s.last = 0
	s.curFG, s.curBG = colorAttr{}, colorAttr{}
	s.pstate = 0
	s.pparams = nil
	s.pprivate = false
	s.history = nil
	s.histSeen = make(map[string]struct{})
	s.lastFrame = nil
}

// Feed parses raw terminal output into the screen and reports whether any
// visible change was made since the flag was last cleared by Sniff.
func (s *Screen) Feed(data []byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.parse(data)
	return s.dirty
}

// Text renders the visible screen as plain text with right-trimmed lines and
// trailing empty rows removed. Runs of three or more blank lines collapse to
// two so the relay message stays compact.
func (s *Screen) Text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.TextNoLock()
}

// TextNoLock is Text without acquiring the mutex; callers must hold it.
func (s *Screen) TextNoLock() string {
	b := s.active()
	var rows []string
	for _, row := range b.rows {
		rows = append(rows, strings.TrimRight(cellsString(row), " "))
	}
	for len(rows) > 0 && strings.TrimSpace(rows[len(rows)-1]) == "" {
		rows = rows[:len(rows)-1]
	}
	var out []string
	blankRun := 0
	for _, r := range rows {
		if strings.TrimSpace(r) == "" {
			blankRun++
			if blankRun > 2 {
				continue
			}
		} else {
			blankRun = 0
		}
		out = append(out, r)
	}
	return strings.Join(out, "\n")
}

func cellsString(row []cell) string {
	var b strings.Builder
	for _, c := range row {
		if c.r != 0 {
			b.WriteRune(c.r)
		}
	}
	return b.String()
}

// frameRows renders the visible rows as captured lines: plain text plus the
// colored cells behind it, right-trimmed and index-aligned, without collapsing
// blanks. Used as the identity snapshot for scrollback capture.
func (s *Screen) frameRows() []Row {
	b := s.active()
	out := make([]Row, len(b.rows))
	for i, row := range b.rows {
		end := len(row)
		for end > 0 && (row[end-1].r == 0 || row[end-1].r == ' ') {
			end--
		}
		cells := make([]RowCell, end)
		var sb strings.Builder
		for j := 0; j < end; j++ {
			cells[j] = RowCell{R: row[j].r, FG: row[j].fg, BG: row[j].bg}
			if row[j].r != 0 {
				sb.WriteRune(row[j].r)
			}
		}
		out[i] = Row{Text: sb.String(), Cells: cells}
	}
	return out
}

// persistScroll moves lines that left the visible window into the scrollback
// transcript, keeping the colors they were drawn with. Each distinct line is
// recorded at most once, so TUI repaints and animations never duplicate
// history; content is committed in the order it was finally replaced. Callers
// must hold the screen mutex.
func (s *Screen) persistScroll() {
	cur := s.frameRows()
	inCur := make(map[string]struct{}, len(cur))
	for _, r := range cur {
		inCur[r.Text] = struct{}{}
	}
	for _, row := range s.lastFrame {
		if _, still := inCur[row.Text]; still {
			continue
		}
		if _, seen := s.histSeen[row.Text]; seen {
			continue
		}
		if utf8.RuneCountInString(strings.TrimSpace(row.Text)) < 3 {
			continue // blanks and transient spinner fragments
		}
		s.history = append(s.history, row)
		s.histSeen[row.Text] = struct{}{}
	}
	s.lastFrame = cur

	if len(s.history) > historyCap {
		drop := len(s.history) - historyCap
		for _, l := range s.history[:drop] {
			delete(s.histSeen, l.Text)
		}
		s.history = s.history[drop:]
	}
}

// viewRows returns every line the TUI has shown so far: the scrollback
// transcript followed by the rows that are still on screen. The live rows are
// appended rather than snapshotted into the transcript, so a reader scrolling
// up from the bottom never hits a gap between what scrolled away and what is
// displayed right now. Callers must hold the screen mutex.
func (s *Screen) viewRows() []Row {
	rows := s.frameRows()
	out := make([]Row, 0, len(s.history)+len(rows))
	out = append(out, s.history...)
	prev := ""
	first := true
	for _, r := range rows {
		if strings.TrimSpace(r.Text) == "" {
			continue // blank filler between blocks carries no information
		}
		if !first && r.Text == prev {
			continue // a status line redrawn in place is not a new line
		}
		out = append(out, r)
		prev = r.Text
		first = false
	}
	return out
}

func (s *Screen) active() *buffer {
	if s.altOn {
		return &s.alt
	}
	return &s.main
}

// --- input handling ---------------------------------------------------------

func (s *Screen) parse(data []byte) {
	str := string(data)
	i := 0
	for i < len(data) {
		c := data[i]
		switch s.pstate {
		case 0:
			// Ground: multibyte UTF-8 must be decoded as one rune, not one
			// byte per cell (TUI box-drawing and emoji would garble).
			if c >= 0x80 {
				r, size := utf8.DecodeRuneInString(str[i:])
				if r == utf8.RuneError && size <= 1 {
					i++ // invalid byte: drop it
					continue
				}
				s.put(r)
				i += size
				continue
			}
			switch {
			case c == 0x1b:
				s.pstate = 1
			case c == 0x9b: // 8-bit CSI
				s.pstate = 2
				s.pparams = nil
				s.pprivate = false
			case c == '\r':
				b := s.active()
				b.cursC = 0
			case c == '\n' || c == 0x0b:
				s.linefeed()
			case c == 0x0c: // form feed clears the screen
				s.clearScreen()
			case c == '\b':
				b := s.active()
				if b.cursC > 0 {
					b.cursC--
				}
			case c == '\t':
				s.tabForward()
			case c == 0x07 || c < 0x20:
				// BEL and other controls are ignored; they are never visible.
			default:
				s.put(rune(c))
			}
		case 1:
			switch c {
			case '[':
				s.pstate = 2
				s.pparams = nil
				s.pprivate = false
			case ']':
				s.pstate = 3
				s.poscStart = i + 1
			case '(':
				s.pstate = -1 // charset select: consume exactly one more byte
			case ')':
				s.pstate = -1
			case '7':
				s.saveCursor()
				s.pstate = 0
			case '8':
				s.restoreCursor()
				s.pstate = 0
			case 'c':
				s.clearScreen()
				s.linefeed()
				s.pstate = 0
			case '=':
				s.pstate = 0
			case '>':
				s.pstate = 0
			case 'M':
				s.reverseIndex()
				s.pstate = 0
			case 'D':
				s.linefeed()
				s.pstate = 0
			default:
				s.pstate = 0
			}
		case -1:
			s.pstate = 0
		case 2:
			switch {
			case c == '?':
				s.pprivate = true
			case c == '>' || c == '!' || c == '=':
				// private marker; ignore
			case c >= '0' && c <= '9':
				if len(s.pparams) == 0 {
					s.pparams = append(s.pparams, 0)
				}
				s.pparams[len(s.pparams)-1] = s.pparams[len(s.pparams)-1]*10 + int(c-'0')
			case c == ';':
				s.pparams = append(s.pparams, 0)
			case c >= 0x20 && c <= 0x2f:
				// intermediate bytes; ignore
			default:
				s.csi(s.pparams, s.pprivate, c)
				s.pstate = 0
			}
		case 3:
			// OSC: terminate at BEL or ST (ESC \).
			if c == 0x07 || (c == 0x1b && i+1 < len(data) && data[i+1] == '\\') {
				_ = s.poscStart
				if c == 0x1b {
					i++
				}
				s.pstate = 0
			}
		}
		i++
	}
}

// put writes a printable rune at the cursor, honouring automatic wrap.
func (s *Screen) put(r rune) {
	b := s.active()
	if b.cursR >= len(b.rows) {
		return
	}
	if s.wrapPending(b) {
		b.cursR++
		if b.cursR >= len(b.rows) {
			s.scrollUp()
			b.cursR = len(b.rows) - 1
		}
		b.cursC = 0
	}
	s.setCell(b, b.cursR, b.cursC, r)
	s.last = r
	b.cursC++
	if b.cursC >= s.colsOf(b) {
		b.cursC = s.colsOf(b) - 1
	}
	s.dirty = true
}

// wrapPending reports whether the previous character ended exactly at the right
// margin, which makes the next printable column wrap to the next line.
func (s *Screen) wrapPending(b *buffer) bool {
	return b.cursC == s.colsOf(b)-1 && s.cellAt(b, b.cursR, b.cursC).r != ' '
}

func (s *Screen) setCell(b *buffer, r, c int, ch rune) {
	if r < 0 || r >= len(b.rows) || c < 0 || c >= maxCols {
		return
	}
	blank := s.blankCell()
	row := b.rows[r]
	if cap(row) < c+1 {
		nrow := make([]cell, c+1, max(c+1, rowCap(len(row))))
		copy(nrow, row)
		for i := len(row); i < c; i++ {
			nrow[i] = blank
		}
		row = nrow
		b.rows[r] = row
	} else if len(row) <= c {
		orig := len(row)
		row = row[:c+1]
		for i := orig; i < c; i++ {
			row[i] = blank
		}
		b.rows[r] = row
	}
	row[c] = cell{r: ch, fg: s.curFG, bg: s.curBG}
}

func (s *Screen) cellAt(b *buffer, r, c int) cell {
	if r < 0 || r >= len(b.rows) || c < 0 || c >= len(b.rows[r]) {
		return cell{r: ' '}
	}
	return b.rows[r][c]
}

func rowCap(n int) int {
	n = n*2 + 16
	if n < screenCols {
		return screenCols
	}
	if n > maxCols {
		return maxCols
	}
	return n
}

func (s *Screen) colsOf(b *buffer) int {
	return screenCols
}

// eraseCell blanks a cell back to a space filled with the current background,
// matching how real terminals erase with the active rendition.
func (s *Screen) eraseCell(b *buffer, r, c int) {
	s.setCell(b, r, c, ' ')
}

// blankCell is the cell written when a region is padded or erased.
func (s *Screen) blankCell() cell {
	return cell{r: ' ', bg: s.curBG}
}

func (s *Screen) eraseLine(b *buffer, mode int) {
	if mode == 0 {
		for c := b.cursC; c < s.colsOf(b); c++ {
			s.eraseCell(b, b.cursR, c)
		}
	} else if mode == 1 {
		for c := 0; c <= b.cursC; c++ {
			s.eraseCell(b, b.cursR, c)
		}
	} else if mode == 2 {
		for c := 0; c < s.colsOf(b); c++ {
			s.eraseCell(b, b.cursR, c)
		}
	}
	s.dirty = true
}

func (s *Screen) clearScreen() {
	b := s.active()
	for r := range b.rows {
		for c := 0; c < s.colsOf(b); c++ {
			s.eraseCell(b, r, c)
		}
	}
	b.cursR, b.cursC = 0, 0
	s.dirty = true
}

func (s *Screen) linefeed() {
	b := s.active()
	b.cursR++
	if b.cursR >= len(b.rows) {
		s.scrollUp()
		b.cursR = len(b.rows) - 1
	}
	s.dirty = true
}

func (s *Screen) reverseIndex() {
	b := s.active()
	if b.cursR == 0 {
		rows := b.rows
		last := rows[len(rows)-1]
		copy(rows[1:], rows[:len(rows)-1])
		rows[0] = blankRow()
		_ = last
		s.dirty = true
		return
	}
	b.cursR--
	s.dirty = true
}

func (s *Screen) scrollUp() {
	b := s.active()
	copy(b.rows, b.rows[1:])
	b.rows[len(b.rows)-1] = blankRow()
}

func (s *Screen) scrollDown() {
	b := s.active()
	copy(b.rows[1:], b.rows[:len(b.rows)-1])
	b.rows[0] = blankRow()
}

func blankRow() []cell {
	return make([]cell, 0, screenCols)
}

func (s *Screen) tabForward() {
	b := s.active()
	c := b.cursC
	next := (c/8 + 1) * 8
	if next >= s.colsOf(b) {
		next = s.colsOf(b) - 1
	}
	b.cursC = next
	s.dirty = true
}

func (s *Screen) tabBackward() {
	b := s.active()
	b.cursC -= (b.cursC % 8) + 1
	if b.cursC < 0 {
		b.cursC = 0
	}
	s.dirty = true
}

func (s *Screen) saveCursor() {
	b := s.active()
	s.savedR, s.savedC, s.savedAlt = b.cursR, b.cursC, s.altOn
}

func (s *Screen) restoreCursor() {
	b := s.active()
	if s.savedAlt != s.altOn {
		if s.savedAlt {
			s.enterAlt()
		} else {
			s.exitAlt()
		}
		b = s.active()
	}
	b.cursR, b.cursC = s.savedR, s.savedC
}

func (s *Screen) enterAlt() {
	if s.altOn {
		return
	}
	s.savedR, s.savedC = s.main.cursR, s.main.cursC
	s.alt = newBuffer()
	s.altOn = true
	s.dirty = true
}

func (s *Screen) exitAlt() {
	if !s.altOn {
		return
	}
	s.alt = newBuffer()
	s.altOn = false
	s.dirty = true
}

// csi dispatches a CSI sequence (ESC [ ... final) with its parameters.
func (s *Screen) csi(params []int, private bool, final byte) {
	p := func(idx, def int) int {
		if idx >= len(params) || params[idx] < 0 {
			return def
		}
		return params[idx]
	}
	// All parameterised moves default to 1.
	move := func(idx int) int { return p(idx, 1) }

	b := s.active()
	switch final {
	case 'A': // cursor up
		b.cursR -= move(0)
		if b.cursR < 0 {
			b.cursR = 0
		}
	case 'B': // cursor down
		b.cursR += move(0)
		if b.cursR >= len(b.rows) {
			b.cursR = len(b.rows) - 1
		}
	case 'C': // cursor forward
		b.cursC += move(0)
		if b.cursC >= s.colsOf(b) {
			b.cursC = s.colsOf(b) - 1
		}
	case 'D': // cursor backward
		b.cursC -= move(0)
		if b.cursC < 0 {
			b.cursC = 0
		}
	case 'E': // cursor next line
		b.cursR += move(0)
		if b.cursR >= len(b.rows) {
			b.cursR = len(b.rows) - 1
		}
		b.cursC = 0
	case 'F': // cursor previous line
		b.cursR -= move(0)
		if b.cursR < 0 {
			b.cursR = 0
		}
		b.cursC = 0
	case 'G': // cursor horizontal absolute
		b.cursC = clamp(p(0, 1)-1, 0, s.colsOf(b)-1)
	case 'd': // vertical position absolute
		b.cursR = clamp(p(0, 1)-1, 0, len(b.rows)-1)
	case 'H', 'f': // cursor position
		b.cursR = clamp(p(0, 1)-1, 0, len(b.rows)-1)
		b.cursC = clamp(p(1, 1)-1, 0, s.colsOf(b)-1)
	case 'J': // erase in display
		switch p(0, 0) {
		case 2, 3:
			s.clearScreen()
		case 1:
			for r := 0; r <= b.cursR; r++ {
				for c := 0; c < s.colsOf(b); c++ {
					if r == b.cursR && c > b.cursC {
						break
					}
					s.eraseCell(b, r, c)
				}
			}
		default:
			for r := b.cursR; r < len(b.rows); r++ {
				start := 0
				if r == b.cursR {
					start = b.cursC
				}
				for c := start; c < s.colsOf(b); c++ {
					s.eraseCell(b, r, c)
				}
			}
		}
	case 'K': // erase in line
		s.eraseLine(b, p(0, 0))
	case 'X': // erase characters
		n := move(0)
		for c := b.cursC; c < s.colsOf(b) && c < b.cursC+n; c++ {
			s.eraseCell(b, b.cursR, c)
		}
	case 'b': // repeat
		s.repeat(move(0))
	case 'P': // delete characters
		s.deleteChars(b, move(0))
	case '@': // insert characters
		s.insertChars(b, move(0))
	case 'L': // insert lines
		s.insertLines(move(0))
	case 'M': // delete lines
		s.deleteLines(move(0))
	case 'S': // scroll up
		n := move(0)
		for i := 0; i < n && i < len(b.rows); i++ {
			s.scrollUp()
		}
	case 'T': // scroll down
		n := move(0)
		for i := 0; i < n && i < len(b.rows); i++ {
			s.scrollDown()
		}
	case 'I': // tab forward
		for i := 0; i < move(0); i++ {
			s.tabForward()
		}
	case 'Z': // tab backward
		for i := 0; i < move(0); i++ {
			s.tabBackward()
		}
	case 'm': // SGR colours/attributes: tracked for image rendering.
		s.applySGR(params)
	case 'n': // device status report: ignored.
	case 't': // window manipulation: ignored.
	case 'r': // scrolling region: unsupported, treated as full screen.
	case 'h': // set mode
		if private {
			switch p(0, 0) {
			case 47, 1047, 1049:
				s.enterAlt()
			}
		}
	case 'l': // reset mode
		if private {
			switch p(0, 0) {
			case 47, 1047, 1049:
				s.exitAlt()
			}
		}
	}
}

// applySGR updates the current rendition from a CSI SGR sequence. Attributes
// that only affect text style (bold, underline, …) are ignored; the colors are
// what the image renderer needs.
func (s *Screen) applySGR(params []int) {
	if len(params) == 0 {
		params = []int{0}
	}
	for i := 0; i < len(params); i++ {
		switch p := params[i]; {
		case p == 0:
			s.curFG, s.curBG = colorAttr{}, colorAttr{}
		case p == 39:
			s.curFG = colorAttr{}
		case p == 49:
			s.curBG = colorAttr{}
		case p >= 30 && p <= 37:
			s.curFG = ansiColor(p-30, false)
		case p >= 90 && p <= 97:
			s.curFG = ansiColor(p-90, true)
		case p >= 40 && p <= 47:
			s.curBG = ansiColor(p-40, false)
		case p >= 100 && p <= 107:
			s.curBG = ansiColor(p-100, true)
		case p == 38 || p == 48:
			// Extended color select: 38;5;n (256 palette) or 38;2;r;g;b.
			if i+1 >= len(params) {
				continue
			}
			switch params[i+1] {
			case 5:
				if i+2 >= len(params) {
					continue
				}
				if p == 38 {
					s.curFG = colorAttr{set: true, rgb: palette256(params[i+2])}
				} else {
					s.curBG = colorAttr{set: true, rgb: palette256(params[i+2])}
				}
				i += 2
			case 2:
				if i+4 >= len(params) {
					continue
				}
				rgb := uint32(clamp(params[i+2], 0, 255)) << 16
				rgb |= uint32(clamp(params[i+3], 0, 255)) << 8
				rgb |= uint32(clamp(params[i+4], 0, 255))
				if p == 38 {
					s.curFG = colorAttr{set: true, rgb: rgb}
				} else {
					s.curBG = colorAttr{set: true, rgb: rgb}
				}
				i += 4
			}
		}
	}
}

var ansiPalette = [16]uint32{
	0x000000, 0xcd0000, 0x00cd00, 0xcdcd00,
	0x0000ee, 0xcd00cd, 0x00cdcd, 0xe5e5e5,
	0x7f7f7f, 0xff0000, 0x00ff00, 0xffff00,
	0x5c5cff, 0xff00ff, 0x00ffff, 0xffffff,
}

// ansiColor is the standard 16-color palette (index 0-7; bright selects 8-15).
func ansiColor(i int, bright bool) colorAttr {
	if bright {
		i += 8
	}
	return colorAttr{set: true, rgb: ansiPalette[i]}
}

// palette256 maps an xterm 256-color index to RGB.
func palette256(n int) uint32 {
	switch {
	case n < 8:
		return ansiPalette[n]
	case n < 16:
		return ansiPalette[n-8+8]
	case n < 232:
		n -= 16
		r, g, bl := n/36, (n%36)/6, n%6
		scale := func(v int) uint32 {
			if v == 0 {
				return 0
			}
			return uint32(55 + v*40)
		}
		return scale(r)<<16 | scale(g)<<8 | scale(bl)
	default:
		v := uint32(8 + (n-232)*10)
		return v<<16 | v<<8 | v
	}
}

func (s *Screen) repeat(n int) {
	if s.last == 0 {
		return
	}
	for i := 0; i < n; i++ {
		s.put(s.last)
	}
}

func (s *Screen) deleteChars(b *buffer, n int) {
	row := b.rows[b.cursR]
	if len(row) < b.cursC {
		return
	}
	from := b.cursC
	to := from + n
	if to > s.colsOf(b) {
		to = s.colsOf(b)
	}
	blank := s.blankCell()
	kept := append([]cell(nil), row[to:len(row)]...)
	for i := 0; i < to-from; i++ {
		kept = append(kept, blank)
	}
	for c := 0; c < s.colsOf(b); c++ {
		if c < len(kept) {
			b.rows[b.cursR][c] = kept[c]
		} else {
			s.eraseCell(b, b.cursR, c)
		}
	}
	s.dirty = true
}

func (s *Screen) insertChars(b *buffer, n int) {
	row := b.rows[b.cursR]
	if len(row) < b.cursC {
		return
	}
	// Shift [cursC, cols-n) right by n, then blank from cursC.
	for c := s.colsOf(b) - 1; c >= b.cursC+n; c-- {
		if c-n < len(row) {
			b.rows[b.cursR][c] = row[c-n]
		} else {
			s.eraseCell(b, b.cursR, c)
		}
	}
	s.eraseCell(b, b.cursR, b.cursC)
	s.dirty = true
}

func (s *Screen) insertLines(n int) {
	b := s.active()
	r := b.cursR
	if r >= len(b.rows) {
		return
	}
	for i := 0; i < n && i < len(b.rows); i++ {
		copy(b.rows[r+1:], b.rows[r:len(b.rows)-1])
		b.rows[r] = blankRow()
	}
	s.dirty = true
}

func (s *Screen) deleteLines(n int) {
	b := s.active()
	r := b.cursR
	if r >= len(b.rows) {
		return
	}
	for i := 0; i < n && i < len(b.rows); i++ {
		copy(b.rows[r:], b.rows[r+1:])
		b.rows[len(b.rows)-1] = blankRow()
	}
	s.dirty = true
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
