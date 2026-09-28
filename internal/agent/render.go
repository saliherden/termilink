package agent

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"sync"
	"unicode/utf8"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Default terminal colors used for cells with no explicit SGR rendition (a
// dark background with light text, like opencode's default theme).
var (
	defaultFG = color.RGBA{0xd6, 0xd4, 0xc9, 0xff}
	defaultBG = color.RGBA{0x12, 0x12, 0x15, 0xff}
)

// Chrome for the scrollback reader: a title strip so the image reads as a
// terminal window, and a dimmer tone for the hint line under it.
var (
	titleBG     = color.RGBA{0x1c, 0x1c, 0x24, 0xff}
	titleAccent = color.RGBA{0x7a, 0xa2, 0xf7, 0xff}
	dimFG       = color.RGBA{0x6b, 0x6b, 0x7b, 0xff}
)

// Font metrics for the screen renderer, computed once at init from the
// embedded Go Mono typeface (BSD-licensed; covers ASCII, box drawing, block
// elements and arrows used by TUI programs).
var (
	renderFace   font.Face
	renderCellW  int
	renderCellH  int
	renderAscent int
)

// CellW and CellH are the renderer's cell size in pixels, exported so a test in
// another package can read a rendered frame back on the grid it was drawn on.
// The values themselves stay unexported: the renderer's callers do not position
// anything on the cell grid, they just get whole images.
func CellW() int { return renderCellW }

// CellH returns the cell height in pixels.
func CellH() int { return renderCellH }

// renderMu serializes every use of renderFace.
//
// font.Face is NOT safe for concurrent use, and opentype.Face is stateful: each
// Glyph call rewrites the face's own sfnt buffer, mask image and vector
// rasterizer. The live screen relay renders from its own goroutine while a
// Telegram command may render the scrollback reader at the same moment; without
// this lock they corrupt each other's rasterizer and the process dies with
// "slice bounds out of range" inside vector.fixedLineTo. The critical sections
// are single glyphs, so contention costs nothing.
var renderMu sync.Mutex

func init() {
	ttf, err := opentype.Parse(gomono.TTF)
	if err != nil {
		panic("agent: parse go mono font: " + err.Error())
	}
	face, err := opentype.NewFace(ttf, &opentype.FaceOptions{
		Size:    14, // font size in points
		DPI:     72,
		Hinting: font.HintingFull,
	})
	if err != nil {
		panic("agent: make go mono face: " + err.Error())
	}
	renderFace = face
	m := renderFace.Metrics()
	renderAscent = fixedRound(m.Ascent)
	descent := fixedRound(m.Descent)
	renderCellH = renderAscent + descent
	if adv, ok := renderFace.GlyphAdvance(' '); ok {
		renderCellW = fixedRound(adv)
	}
	if renderCellW < 6 {
		renderCellW = 6
	}
	if renderCellH < 12 {
		renderCellH = 12
	}
}

func fixedRound(v fixed.Int26_6) int {
	return (int(v) + 32) / 64
}

// PNG renders the visible screen as a colored PNG. Trailing blank rows and
// columns are trimmed so full-width logo screens stay compact. The returned
// bytes are a ready-to-send PNG image.
//
// Trimming is not a compromise to work around — it is what makes the render
// readable. An agent screen is 100 columns wide, which is ~800px; Telegram scales
// a photo down to the chat bubble, roughly 300px on a phone, and a full-width
// frame lands at about 3px per character cell. Trimmed to the columns that
// actually hold something the image is narrow, and the bubble enlarges it back to
// something legible. It also keeps the live relay cheap, since a screen holding
// two cells costs 2 cells of bandwidth instead of 100.
//
// This render is for the live screen only. The screen posted when a session ends
// is text — it is written output rather than a picture of one, so it stays
// copyable and searchable — and never comes through here.
func (s *Screen) PNG() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.PNGNoLock()
}

// PNGNoLock is PNG without acquiring the mutex; callers must hold it.
func (s *Screen) PNGNoLock() ([]byte, error) {
	b := s.active()
	visRows, visCols := s.visibleExtent(b)
	if visRows == 0 || visCols == 0 {
		visRows, visCols = 1, 1
	}
	return s.pngNoLock(visRows, visCols)
}

// pngNoLock encodes the visible window at the given extent, the content
// bounding box that PNGNoLock computed. Callers must hold the mutex.
func (s *Screen) pngNoLock(visRows, visCols int) ([]byte, error) {
	b := s.active()

	img := image.NewRGBA(image.Rect(0, 0, visCols*renderCellW, visRows*renderCellH))
	fillRGBA(img, 0, 0, img.Bounds().Dx(), img.Bounds().Dy(), defaultBG)

	for r := 0; r < visRows; r++ {
		drawCells(img, r*renderCellH, b.rows[r], visCols)
	}

	// Draw the cursor as an underline.
	if b.cursR < visRows && b.cursC < visCols {
		x0, y0 := b.cursC*renderCellW, (b.cursR+1)*renderCellH-2
		fillRGBA(img, x0, y0, renderCellW, 2, defaultFG)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// drawCells paints one row of cells at y0, honoring each cell's background and
// foreground. toCell adapts a source cell type to the shared cell shape, so the
// live screen and the scrollback reader draw through the same code.
func drawCells(img *image.RGBA, y0 int, cells []cell, cols int) {
	for c := 0; c < cols; c++ {
		var cl cell
		if c < len(cells) {
			cl = cells[c]
		}
		x0 := c * renderCellW
		if bg := toRGBA(cl.bg, defaultBG); bg != defaultBG {
			fillRGBA(img, x0, y0, renderCellW, renderCellH, bg)
		}
		if cl.r != 0 && cl.r != ' ' {
			drawGlyph(img, cl.r, x0, y0, toRGBA(cl.fg, defaultFG))
		}
	}
}

func toScreenCell(c RowCell) cell { return cell{r: c.R, fg: c.FG, bg: c.BG} }

// drawGlyph paints one glyph. It takes renderMu because renderFace is shared
// state — see the note on renderMu.
func drawGlyph(img *image.RGBA, r rune, x0, y0 int, fg color.RGBA) {
	renderMu.Lock()
	defer renderMu.Unlock()
	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(fg),
		Face: renderFace,
		Dot:  fixed.P(x0, y0+renderAscent),
	}
	d.DrawString(string(r))
}

// RenderRowsPNG renders captured scrollback rows as one image that looks like
// the terminal they came from: per-cell colors are preserved and a title bar on
// top names the view. footer is drawn as a dim last line when non-empty.
func RenderRowsPNG(rows []Row, title string, footer string) ([]byte, error) {
	if len(rows) == 0 {
		rows = []Row{{}}
	}
	visCols := 0
	for _, r := range rows {
		if len(r.Cells) > visCols {
			visCols = len(r.Cells)
		}
	}
	for _, s := range []string{title, footer} {
		if n := utf8.RuneCountInString(s); n > visCols {
			visCols = n
		}
	}
	if visCols == 0 {
		visCols = 1
	}

	bars := 0
	if title != "" {
		bars++
	}
	if footer != "" {
		bars++
	}
	visRows := len(rows) + bars

	img := image.NewRGBA(image.Rect(0, 0, visCols*renderCellW, visRows*renderCellH))
	fillRGBA(img, 0, 0, img.Bounds().Dx(), img.Bounds().Dy(), defaultBG)

	y := 0
	if title != "" {
		drawBand(img, y, visCols, titleBG)
		drawText(img, y, 1, title, visCols, titleAccent)
		y += renderCellH
	}
	for _, r := range rows {
		cells := make([]cell, len(r.Cells))
		for i, c := range r.Cells {
			cells[i] = toScreenCell(c)
		}
		drawCells(img, y, cells, visCols)
		y += renderCellH
	}
	if footer != "" {
		drawText(img, y, 1, footer, visCols, dimFG)
		y += renderCellH
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// drawBand fills one text row with a solid color, used for the title strip.
func drawBand(img *image.RGBA, y0, cols int, bg color.RGBA) {
	fillRGBA(img, 0, y0, cols*renderCellW, renderCellH, bg)
}

// drawText draws a single line of text in one color, starting at column col.
func drawText(img *image.RGBA, y0, col int, s string, cols int, fg color.RGBA) {
	for i, r := range s {
		c := col + i
		if c >= cols || r == 0 || r == ' ' {
			continue
		}
		drawGlyph(img, r, c*renderCellW, y0, fg)
	}
}

// visibleExtent returns the trimmed size: the number of rows and columns that
// contain any visible content, dropping trailing all-blank rows and trailing
// blank columns on every row. A cell is visible when it holds a printable rune
// or carries an explicit background (a filled color zone).
func (s *Screen) visibleExtent(b *buffer) (rows, cols int) {
	vis := func(r int, c int) bool {
		cl := s.cellAt(b, r, c)
		return (cl.r != 0 && cl.r != ' ') || cl.bg.set
	}
	for r := len(b.rows) - 1; r >= 0; r-- {
		last := 0
		for c := len(b.rows[r]) - 1; c >= 0; c-- {
			if vis(r, c) {
				last = c + 1
				break
			}
		}
		if last > cols {
			cols = last
		}
		if last > 0 && r+1 > rows {
			rows = r + 1
		}
	}
	return rows, cols
}

func toRGBA(a colorAttr, def color.RGBA) color.RGBA {
	if !a.set {
		return def
	}
	return color.RGBA{uint8(a.rgb >> 16), uint8(a.rgb >> 8), uint8(a.rgb), 0xff}
}

func fillRGBA(img *image.RGBA, x, y, w, h int, c color.RGBA) {
	for yy := y; yy < y+h; yy++ {
		for xx := x; xx < x+w; xx++ {
			img.SetRGBA(xx, yy, c)
		}
	}
}
