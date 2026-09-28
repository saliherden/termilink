package telegram

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	tg "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/saliherden/termilink/internal/agent"
)

// fakeDoc is one uploaded document, with just enough of the file recorded to
// prove the handler sent real image bytes rather than an empty upload.
type fakeDoc struct {
	filename string
	head     []byte
	// body is the whole upload. head is enough to check a magic number, but a
	// test that has to know *what the image shows* has to decode it, and these
	// fixtures are a few KB each.
	body []byte
	// disableTypeDetection records the raw flag. The upload part's own
	// content type is always application/octet-stream because the library calls
	// multipart.CreateFormFile and offers no way to override it, so the flag is
	// the only thing that decides whether Telegram shows a preview or a bare
	// file icon.
	disableTypeDetection string
}

// pngInk reads an uploaded PNG as text, one character per terminal cell, so a
// test can assert on what the owner actually sees in the picture rather than on
// the caption beside it. Glyph shapes are lost — a cell with any ink reads as
// "#" — which is enough to tell a line of conversation from a banner.
//
// The cell grid is walked in the renderer's own geometry, and a cell counts as
// ink when it differs from the cell's own background pixel rather than from a
// single global colour: a TUI paints panels, so there is no one background to
// compare against, and comparing against the image origin reads every cell as
// blank on any screen that does not start with background.
func (d fakeDoc) pngInk() (string, error) {
	img, err := png.Decode(bytes.NewReader(d.body))
	if err != nil {
		return "", err
	}
	b := img.Bounds()
	cols := b.Dx() / agent.CellW()
	rows := b.Dy() / agent.CellH()
	if cols == 0 || rows == 0 {
		return "", fmt.Errorf("image is %dx%d, smaller than one %dx%d cell",
			b.Dx(), b.Dy(), agent.CellW(), agent.CellH())
	}
	var out strings.Builder
	for r := 0; r < rows; r++ {
		var line strings.Builder
		for c := 0; c < cols; c++ {
			x0, y0 := b.Min.X+c*agent.CellW(), b.Min.Y+r*agent.CellH()
			bg := img.At(x0, y0)
			ink := false
			for y := y0; y < y0+agent.CellH() && !ink; y++ {
				for x := x0; x < x0+agent.CellW(); x++ {
					if img.At(x, y) != bg {
						ink = true
						break
					}
				}
			}
			if ink {
				line.WriteByte('#')
			} else {
				line.WriteByte(' ')
			}
		}
		if strings.TrimSpace(line.String()) != "" {
			out.WriteString(line.String())
			out.WriteByte('\n')
		}
	}
	return out.String(), nil
}

// fakeTelegram is an httptest stand-in for the Bot API. It records the text of
// every message the handler sends, so tests can assert on what the owner
// actually sees instead of on internal calls.
//
// It separates photos from documents because the two are not interchangeable
// for the reader: the live relay sends a photo every frame, while a final screen
// goes out as a document so the 100-column frame opens at full size instead of
// being squeezed into a chat bubble. A test that only counted "an image was
// sent" would pass for the wrong upload method.
type fakeTelegram struct {
	mu        sync.Mutex
	sent      []string
	captions  []string
	photos    []string
	documents []fakeDoc
	// mediaEdits counts editMessageMedia calls and records what type of media
	// each one carried, so a test can tell a repainted document from a repainted
	// photo. The scrollback reader relies on this: it must stay one message.
	mediaEdits []string
	srv        *httptest.Server
	bot        *tg.Bot
}

func newFakeTelegram(t *testing.T) *fakeTelegram {
	t.Helper()
	f := &fakeTelegram{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The library posts multipart form data, not JSON.
		var text string
		if err := r.ParseMultipartForm(1 << 20); err == nil {
			text = r.FormValue("text")
			f.mu.Lock()
			// Photo captions are how the relay reports a screen; document
			// captions are how the exit paths report the final one.
			if c := r.FormValue("caption"); strings.TrimSpace(c) != "" {
				f.captions = append(f.captions, c)
			}
			// File parts live in MultipartForm.File, not in the form values:
			// FormValue returns "" for an upload regardless of its name. Keep the
			// upload's own filename so a test can tell which screen it is, without
			// racing the relay that uploads its own photo every tick.
			for _, fhs := range r.MultipartForm.File["photo"] {
				f.photos = append(f.photos, fhs.Filename)
			}
			// editMessageMedia posts the new media as a JSON `media` field plus one
			// attach:// upload part, so the declared type is what identifies it.
			if raw := r.FormValue("media"); strings.TrimSpace(raw) != "" {
				var m struct {
					Type string `json:"type"`
				}
				kind := "unknown"
				if json.Unmarshal([]byte(raw), &m) == nil && m.Type != "" {
					kind = m.Type
				}
				f.mediaEdits = append(f.mediaEdits, kind)
			}
			if file, fh, err := r.FormFile("document"); err == nil {
				body, _ := io.ReadAll(file)
				f.documents = append(f.documents, fakeDoc{
					filename:             fh.Filename,
					head:                 readHead(body, 8),
					body:                 body,
					disableTypeDetection: r.FormValue("disable_content_type_detection"),
				})
				_ = file.Close()
			}
			f.mu.Unlock()
		} else {
			var req struct {
				Text    string `json:"text"`
				Caption string `json:"caption"`
			}
			if json.NewDecoder(r.Body).Decode(&req) == nil {
				text = req.Text
			}
		}
		if strings.TrimSpace(text) != "" {
			f.mu.Lock()
			f.sent = append(f.sent, text)
			f.mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"date":0,"chat":{"id":1,"type":"private"}}}`))
	}))
	bot, err := tg.New("123456:fake-token",
		tg.WithServerURL(f.srv.URL),
		tg.WithSkipGetMe(),
		tg.WithNotAsyncHandlers(),
	)
	if err != nil {
		f.srv.Close()
		t.Fatalf("fake bot: %v", err)
	}
	f.bot = bot
	t.Cleanup(f.srv.Close)
	return f
}

// readHead returns the first n bytes of an uploaded file, so a test can check the
// PNG magic bytes and prove real image data was sent.
func readHead(body []byte, n int) []byte {
	if len(body) < n {
		return body
	}
	return body[:n]
}

func (f *fakeTelegram) messages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}

// saw reports whether any recorded message contains substr. Photo captions count
// too: the relay reports screens as images, so a test that only watched plain
// text would miss what the owner actually sees.
func (f *fakeTelegram) saw(substr string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range append(append([]string(nil), f.sent...), f.captions...) {
		if strings.Contains(m, substr) {
			return true
		}
	}
	return false
}

// sawPhoto reports whether anything was sent as a picture rather than as a
// document or as text.
func (f *fakeTelegram) sawPhoto() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.photos) > 0
}

// sawPhotoNamed reports whether a photo was uploaded under the given filename.
// Matching on the name rather than counting is what keeps a test honest while
// the live relay is still running and uploading agent.png every tick.
func (f *fakeTelegram) sawPhotoNamed(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, got := range f.photos {
		if got == name {
			return true
		}
	}
	return false
}

// sawDocument reports whether anything was sent as a document.
func (f *fakeTelegram) sawDocument() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.documents) > 0
}

// uploadedDocs returns copies of the recorded document uploads.
func (f *fakeTelegram) uploadedDocs() []fakeDoc {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeDoc(nil), f.documents...)
}

// mediaEditTypes returns the media type of every editMessageMedia call, in order.
func (f *fakeTelegram) mediaEditTypes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.mediaEdits...)
}

// exitsPosted counts how many times an agent-exit announcement was delivered, so
// a test can prove a single closure was not reported twice.
func (f *fakeTelegram) exitsPosted() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int
	for _, m := range append(append([]string(nil), f.sent...), f.captions...) {
		if strings.Contains(m, "Agent exited") {
			n++
		}
	}
	return n
}

var _ = models.Message{}
