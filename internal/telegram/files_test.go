package telegram

import (
	"archive/tar"
	"archive/zip"
	"context"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saliherden/termilink/internal/session"
)

func TestSanitizeUploadFilename(t *testing.T) {
	cases := []struct{ in, want string }{
		{"app.txt", "app.txt"},
		{"/etc/passwd", "passwd"},
		{"../../x.sh", "x.sh"},
		{"dir\\file.txt", "file.txt"},
		{"release-notes (1).md", "release-notes (1).md"},
		{"..", ""},
		{".", ""},
		{"", ""},
		{"traversal..file", ""},
		{strings.Repeat("a", 256), ""},
	}
	for _, c := range cases {
		if got := sanitizeUploadFilename(c.in); got != c.want {
			t.Errorf("sanitizeUploadFilename(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestGetPathOrFilter(t *testing.T) {
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "sub", "real.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(Options{})
	st := &session.State{Cwd: cwd}

	for _, p := range []string{"/abs/path", "~/rel"} {
		if !h.getPathOrFilter(st, p) {
			t.Errorf("getPathOrFilter(%q) = false, want true", p)
		}
	}
	for _, p := range []string{"debug", "release", "apk", ""} {
		if h.getPathOrFilter(st, p) {
			t.Errorf("getPathOrFilter(%q) = true, want false", p)
		}
	}
	if !h.getPathOrFilter(st, "./sub/real.txt") {
		t.Fatal("existing dotted path must be a path")
	}
	if h.getPathOrFilter(st, ".md") {
		t.Fatal("missing dotted keyword must fall through to filter")
	}
	if !h.getPathOrFilter(st, "sub/real.txt") {
		t.Fatal("existing relative file must be a path")
	}
	if h.getPathOrFilter(st, "sub/missing.txt") {
		t.Fatal("missing slash path must fall through to filter")
	}
	if h.getPathOrFilter(st, "release/14") {
		t.Fatal("filter keyword with slash (no such path) must stay a filter")
	}
}

func TestResolveTargetPath(t *testing.T) {
	t.Setenv("HOME", "/home/test")
	h := NewHandler(Options{})

	abs, err := h.resolveTargetPath(&session.State{}, "/etc/hosts")
	if err != nil || abs != "/etc/hosts" {
		t.Fatalf("abs path: %q, %v", abs, err)
	}
	home, _ := h.resolveTargetPath(&session.State{}, "~/x/y")
	if home != "/home/test/x/y" {
		t.Fatalf("~ path: %q", home)
	}
	doh, _ := h.resolveTargetPath(&session.State{}, "$HOME/z")
	if doh != "/home/test/z" {
		t.Fatalf("$HOME path: %q", doh)
	}
	st := &session.State{Cwd: "/proj"}
	rel, _ := h.resolveTargetPath(st, "docs/readme.md")
	if rel != "/proj/docs/readme.md" {
		t.Fatalf("relative path: %q", rel)
	}
}

func TestFindArtifactsAndFilter(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string, mod time.Time) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		os.Chtimes(p, mod, mod)
	}
	older := time.Now().Add(-time.Hour)
	newer := time.Now()
	mk("dist/app-debug.apk", newer)
	mk("dist/app-release.apk", older)
	mk("baseline/app-debug.apk", older)
	mk("raw/build.bin", newer)

	all, err := findArtifacts(root, []string{"*.apk"})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("want 3 apks, got %d", len(all))
	}
	if all[0].Name != "app-debug.apk" {
		t.Fatalf("newest first: got %s", all[0].Name)
	}
	if got := filterArtifacts(all, "debug"); len(got) != 2 {
		t.Fatalf("filter debug want 2, got %d", len(got))
	}
	if got := filterArtifacts(all, "release"); len(got) != 1 {
		t.Fatalf("filter release want 1, got %d", len(got))
	}
	if got := filterArtifacts(all, ""); len(got) != 3 {
		t.Fatalf("empty filter keeps all: %d", len(got))
	}

	distOnly, err := findArtifacts(root, []string{"dist/*.apk"})
	if err != nil {
		t.Fatal(err)
	}
	if len(distOnly) != 2 {
		t.Fatalf("dist/*.apk want 2, got %d", len(distOnly))
	}
}

func TestZipToTemp(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "input.bin")
	content := "hello-termilink-content"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	zipPath, size, err := zipToTemp(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(zipPath)
	if size <= 0 {
		t.Fatalf("zero zip size")
	}
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	if len(zr.File) != 1 || zr.File[0].Name != "input.bin" {
		t.Fatalf("unexpected archive contents: %+v", zr.File)
	}
	rc, _ := zr.File[0].Open()
	data, _ := io.ReadAll(rc)
	rc.Close()
	if string(data) != content {
		t.Fatalf("zip roundtrip mismatch: %q", string(data))
	}
}

func TestTarToTemp(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "input.bin")
	content := "hello-termilink-content"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	tarPath, err := tarToTemp(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tarPath)
	if !strings.HasSuffix(tarPath, ".tar") {
		t.Fatalf("unexpected archive name: %q", tarPath)
	}
	tr, err := os.Open(tarPath)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	r := tar.NewReader(tr)
	hdr, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Name != "input.bin" {
		t.Fatalf("unexpected archive entry: %+v", hdr)
	}
	data, _ := io.ReadAll(r)
	if string(data) != content {
		t.Fatalf("tar roundtrip mismatch: %q", string(data))
	}
}

func TestDeliverFileTooBigNoHost(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "random.bin")
	payload := make([]byte, 8192)
	if _, err := io.ReadFull(rand.Reader, payload); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(Options{MaxFileBytes: 256})
	_, err := h.deliverFile(context.Background(), nil, 1, 1, path, "")
	if err == nil {
		t.Fatal("want limit error, got nil")
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDeliverFileEnqueuesLinkApproval(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "random.bin")
	payload := make([]byte, 8192)
	if _, err := io.ReadFull(rand.Reader, payload); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(Options{MaxFileBytes: 256, BigFileLink: "uguu.se"})
	note, err := h.deliverFile(context.Background(), nil, 42, 7, path, "")
	if err != nil {
		t.Fatalf("link approval should enqueue, got error: %v", err)
	}
	if note != "" {
		t.Fatalf("unexpected note: %q", note)
	}
	p := h.filesPendingFor(42)
	if p == nil {
		t.Fatal("expected a pending link approval")
	}
	if p.display != "random.bin" || p.host != "uguu.se" || p.userID != 7 {
		t.Fatalf("unexpected pending file: %+v", p)
	}
	if !strings.HasSuffix(p.path, ".tar") {
		t.Fatalf("link approval must upload a .tar archive, got %q", p.path)
	}
	if _, err := os.Stat(p.path); err != nil {
		t.Fatalf("archive temp must exist while pending: %v", err)
	}
}

func TestDownloadTGFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("payload-data"))
	}))
	defer srv.Close()

	body, err := downloadTGFile(context.Background(), srv.URL, 100)
	if err != nil || string(body) != "payload-data" {
		t.Fatalf("download: %q, %v", body, err)
	}
	if _, err := downloadTGFile(context.Background(), srv.URL, 5); err == nil {
		t.Fatal("want size-limit error, got nil")
	}
}

func TestSavedPathNoClobber(t *testing.T) {
	dir := t.TempDir()
	first := savedPathNoClobber(dir, "a.txt")
	if first != filepath.Join(dir, "a.txt") {
		t.Fatalf("first: %q", first)
	}
	if err := os.WriteFile(first, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	second := savedPathNoClobber(dir, "a.txt")
	if second != filepath.Join(dir, "a-1.txt") {
		t.Fatalf("second: %q", second)
	}
}

func TestHumanSize(t *testing.T) {
	if humanSize(512) != "512B" || humanSize(1<<20) != "1.0MB" {
		t.Fatalf("humanSize: %s / %s", humanSize(512), humanSize(1<<20))
	}
}
