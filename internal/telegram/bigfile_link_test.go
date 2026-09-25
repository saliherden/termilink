package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saliherden/termilink/internal/audit"
	"github.com/saliherden/termilink/internal/security"
)

func TestParseUguuResponse(t *testing.T) {
	good := []byte(`{"success":true,"files":[{"hash":"abc","filename":"x.bin","url":"https:\/\/h.uguu.se\/abc.bin","size":125829120}],"errors":[]}`)
	url, err := parseUguuResponse(good)
	if err != nil || url != "https://h.uguu.se/abc.bin" {
		t.Fatalf("parse uguu: %q, %v", url, err)
	}
	if _, err := parseUguuResponse([]byte(`{"success":false,"files":[],"errors":["too huge"]}`)); err == nil || !strings.Contains(err.Error(), "too huge") {
		t.Fatalf("want error surfaced, got %v", err)
	}
	if _, err := parseUguuResponse([]byte(`not json`)); err == nil {
		t.Fatal("want error on garbage response")
	}
}

func TestParsePlainURL(t *testing.T) {
	url, err := parsePlainURL([]byte("https://files.catbox.moe/abc.txt\n"))
	if err != nil || url != "https://files.catbox.moe/abc.txt" {
		t.Fatalf("parse plain: %q, %v", url, err)
	}
	if _, err := parsePlainURL([]byte("  ")); err == nil {
		t.Fatal("want error on empty response")
	}
}

func TestUploadToLinkHostAdapters(t *testing.T) {
	want := bytes.Repeat([]byte{0xAB}, 4000)
	f := filepath.Join(t.TempDir(), "payload.bin")
	if err := os.WriteFile(f, want, 0o644); err != nil {
		t.Fatal(err)
	}

	uguu := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		got, _, err := r.FormFile("files[]")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer got.Close()
		buf := new(bytes.Buffer)
		if _, err := buf.ReadFrom(got); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !bytes.Equal(buf.Bytes(), want) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"files":   []map[string]any{{"url": "https://h.uguu.se/xyz"}},
			"errors":  []string{},
		})
	}))
	defer uguu.Close()

	catbox := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.FormValue("reqtype") != "fileupload" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		got, _, err := r.FormFile("fileToUpload")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer got.Close()
		buf := new(bytes.Buffer)
		if _, err := buf.ReadFrom(got); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !bytes.Equal(buf.Bytes(), want) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte("https://files.catbox.moe/abc.txt"))
	}))
	defer catbox.Close()

	original := linkHostBase
	linkHostBase = map[string]string{"uguu.se": uguu.URL, "catbox.moe": catbox.URL}
	defer func() { linkHostBase = original }()

	url, err := uploadToLinkHost(context.Background(), "uguu.se", f)
	if err != nil || url != "https://h.uguu.se/xyz" {
		t.Fatalf("uguu.se upload: %q, %v", url, err)
	}
	url, err = uploadToLinkHost(context.Background(), "catbox.moe", f)
	if err != nil || url != "https://files.catbox.moe/abc.txt" {
		t.Fatalf("catbox.moe upload: %q, %v", url, err)
	}
	if url, err := uploadToLinkHost(context.Background(), "unknown", f); err == nil || url != "" {
		t.Fatalf("unknown host should error: %q, %v", url, err)
	}

	errHost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusTooManyRequests)
	}))
	defer errHost.Close()
	linkHostBase = map[string]string{"uguu.se": errHost.URL}
	defer func() { linkHostBase = original }()
	if _, err := uploadToLinkHost(context.Background(), "uguu.se", f); err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("want HTTP 429 error, got %v", err)
	}
}

func mustLinkHandler(t *testing.T, auditPath string) *Handler {
	t.Helper()
	l, err := audit.Open(auditPath)
	if err != nil {
		t.Fatalf("open audit: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	return NewHandler(Options{
		MaxFileBytes: 1 << 20,
		BigFileLink:  "uguu.se",
		Authorizer:   security.New(1, []int64{1}),
		Audit:        l,
	})
}

func TestResolveFileApprovalDenyTimeoutBlock(t *testing.T) {
	h := mustLinkHandler(t, filepath.Join(t.TempDir(), "audit.log"))
	chat, user := int64(11), int64(1)

	f := filepath.Join(t.TempDir(), "big.bin")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// non-owner answer stays pending and never uploads.
	h.filePendingMu.Lock()
	h.filesPending[chat] = pendingFile{userID: user, path: f, size: 100, display: "big.bin", chatID: chat, host: "uguu.se", expires: time.Now().Add(time.Hour)}
	h.filePendingMu.Unlock()
	h.resolveFileApproval(context.Background(), nil, chat, 999, "evet")
	if h.filesPendingFor(chat) == nil {
		t.Fatal("non-owner answer should keep request pending")
	}

	// owner deny consumes request.
	h.resolveFileApproval(context.Background(), nil, chat, user, "hayır")
	if h.filesPendingFor(chat) != nil {
		t.Fatal("owner deny should consume request")
	}

	// expired request is treated as timeout and cleared.
	h.filePendingMu.Lock()
	h.filesPending[chat] = pendingFile{userID: user, path: f, size: 100, display: "big.bin", chatID: chat, host: "uguu.se", expires: time.Now().Add(-time.Hour)}
	h.filePendingMu.Unlock()
	h.resolveFileApproval(context.Background(), nil, chat, user, "evet")
	if h.filesPendingFor(chat) != nil {
		t.Fatal("expired request should be cleared")
	}
}

func TestResolveFileApprovalApprove(t *testing.T) {
	h := mustLinkHandler(t, filepath.Join(t.TempDir(), "audit.log"))
	chat, user := int64(22), int64(1)
	f := filepath.Join(t.TempDir(), "big.bin")
	if err := os.WriteFile(f, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}

	original := linkUpload
	defer func() { linkUpload = original }()
	var uploads []string
	linkUpload = func(_ context.Context, host, path string) (string, error) {
		uploads = append(uploads, path)
		return "https://h.uguu.se/stub/" + host, nil
	}

	h.filePendingMu.Lock()
	h.filesPending[chat] = pendingFile{userID: user, path: f, size: 100, display: "big.bin", chatID: chat, host: "uguu.se", expires: time.Now().Add(time.Hour)}
	h.filePendingMu.Unlock()

	h.resolveFileApproval(context.Background(), nil, chat, user, "evet")
	if h.filesPendingFor(chat) != nil {
		t.Fatal("approval should consume request")
	}
	if len(uploads) != 1 || uploads[0] != f {
		t.Fatalf("upload stub not called as expected: %v", uploads)
	}

	// upload failure is surfaced and still consumes the request.
	h.filePendingMu.Lock()
	h.filesPending[chat] = pendingFile{userID: user, path: f, size: 100, display: "big.bin", chatID: chat, host: "uguu.se", expires: time.Now().Add(time.Hour)}
	h.filePendingMu.Unlock()
	linkUpload = func(_ context.Context, _, _ string) (string, error) {
		return "", os.ErrPermission
	}
	h.resolveFileApproval(context.Background(), nil, chat, user, "evet")
	if h.filesPendingFor(chat) != nil {
		t.Fatal("failed upload still consumes approval")
	}
}

func TestDeliverFileDoubleApprovalRejected(t *testing.T) {
	h := mustLinkHandler(t, filepath.Join(t.TempDir(), "audit.log"))
	dir := t.TempDir()
	big := filepath.Join(dir, "random.bin")
	payload := make([]byte, 8192)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	if err := os.WriteFile(big, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	h.filePendingMu.Lock()
	h.filesPending[33] = pendingFile{userID: 1, path: big, size: 1, display: "random.bin", chatID: 33, host: "uguu.se", expires: time.Now().Add(time.Hour)}
	h.filePendingMu.Unlock()

	_, err := h.enqueueLinkApproval(context.Background(), nil, 33, 1, big, big, 8192)
	if err == nil {
		t.Fatal("second queued approval while one is pending must error")
	}
}
