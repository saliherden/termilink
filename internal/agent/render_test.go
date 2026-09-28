package agent

import (
	"bytes"
	"image/png"
	"testing"
)

func TestScreenPNGValid(t *testing.T) {
	sc := feedScreen(t, "hello")
	img, err := sc.PNG()
	if err != nil {
		t.Fatalf("png: %v", err)
	}
	if !bytes.HasPrefix(img, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("not a PNG: %x", img[:8])
	}
}

func TestScreenPNGDimensions(t *testing.T) {
	sc := feedScreen(t, "abc")
	img, err := sc.PNG()
	if err != nil {
		t.Fatal(err)
	}
	dec, err := png.Decode(bytes.NewReader(img))
	if err != nil {
		t.Fatal(err)
	}
	wantW, wantH := 3*renderCellW, 1*renderCellH
	if dec.Bounds().Dx() != wantW || dec.Bounds().Dy() != wantH {
		t.Fatalf("png size = %dx%d, want %dx%d", dec.Bounds().Dx(), dec.Bounds().Dy(), wantW, wantH)
	}
}

func TestScreenPNGColors(t *testing.T) {
	// A blue-filled cell followed by a red glyph on the default background.
	sc := feedScreen(t, "\x1b[48;2;0;0;255m \x1b[38;2;255;0;0mX\x1b[0m")
	img, err := sc.PNG()
	if err != nil {
		t.Fatal(err)
	}
	dec, err := png.Decode(bytes.NewReader(img))
	if err != nil {
		t.Fatal(err)
	}

	if r, g, b, _ := dec.At(renderCellW/2, 0).RGBA(); uint8(r>>8) != 0 || uint8(g>>8) != 0 || uint8(b>>8) != 255 {
		t.Fatalf("bg cell not blue: %v", dec.At(renderCellW/2, 0))
	}

	// Somewhere inside the second cell should be the red glyph pixels.
	red := false
	for y := 0; y < renderCellH && !red; y++ {
		for x := renderCellW; x < 2*renderCellW; x++ {
			if r, g, b, _ := dec.At(x, y).RGBA(); uint8(r>>8) == 255 && uint8(g>>8) == 0 && uint8(b>>8) == 0 {
				red = true
				break
			}
		}
	}
	if !red {
		t.Fatal("no red glyph pixels found in the second cell")
	}
}

func TestScreenPNGTrimsBlanks(t *testing.T) {
	sc := feedScreen(t, "hi")
	img, err := sc.PNG()
	if err != nil {
		t.Fatal(err)
	}
	dec, err := png.Decode(bytes.NewReader(img))
	if err != nil {
		t.Fatal(err)
	}
	if dec.Bounds().Dx() != 2*renderCellW {
		t.Fatalf("width = %d, want 2 cells", dec.Bounds().Dx())
	}
}

func TestPalette256(t *testing.T) {
	cases := map[int]uint32{
		0:   0x000000,
		7:   0xe5e5e5,
		8:   0x7f7f7f,
		15:  0xffffff,
		196: 0xff0000,
		46:  0x00ff00,
		21:  0x0000ff,
		231: 0xffffff,
		232: 0x080808,
		255: 0xeeeeee,
	}
	for n, want := range cases {
		if got := palette256(n); got != want {
			t.Errorf("palette256(%d) = %06x, want %06x", n, got, want)
		}
	}
}

func TestScreenPNGBlank(t *testing.T) {
	sc := NewScreen()
	img, err := sc.PNG()
	if err != nil {
		t.Fatal(err)
	}
	dec, err := png.Decode(bytes.NewReader(img))
	if err != nil {
		t.Fatal(err)
	}
	if dec.Bounds().Dx() < renderCellW || dec.Bounds().Dy() < renderCellH {
		t.Fatalf("blank png too small: %dx%d", dec.Bounds().Dx(), dec.Bounds().Dy())
	}
}
