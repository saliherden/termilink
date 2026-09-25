package telegram

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tg "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/saliherden/termilink/internal/session"
)

const (
	defaultMaxFileBytes = 50 << 20 // 50 MiB, Telegram document limit
	maxArtifactsShare   = 15
	fileDownloadTimeout = 2 * time.Minute
)

type artifactFile struct {
	Path string
	Name string
	Rel  string
	Size int64
	Mod  time.Time
}

func (h *Handler) uploadLimit() int64 {
	if h.maxFileBytes > 0 {
		return h.maxFileBytes
	}
	return defaultMaxFileBytes
}

func humanSize(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%dB", n)
	}
	mib := float64(n) / (1 << 20)
	return fmt.Sprintf("%.1fMB", mib)
}

// getPathOrFilter decides whether a /get argument is a filesystem path (path
// mode) or an artifact filter keyword (filter mode). Prefixes /, ~, $ and a
// leading . that resolves to an existing file mean a path; a plain keyword
// (no separator) is a filter; a slash-separated argument is a path only when
// it resolves to an existing file.
func (h *Handler) getPathOrFilter(st *session.State, target string) bool {
	if strings.HasPrefix(target, "/") || strings.HasPrefix(target, "~") || strings.HasPrefix(target, "$") {
		return true
	}
	if strings.HasPrefix(target, ".") {
		abs, err := h.resolveTargetPath(st, target)
		if err != nil {
			return false
		}
		info, err := os.Stat(abs)
		return err == nil && !info.IsDir()
	}
	if !strings.Contains(target, "/") && !strings.Contains(target, "\\") {
		return false
	}
	abs, err := h.resolveTargetPath(st, target)
	if err != nil {
		return false
	}
	info, err := os.Stat(abs)
	return err == nil && !info.IsDir()
}

// resolveTargetPath expands ~ / $HOME and absolute paths, and resolves relative
// paths against the current working directory.
func (h *Handler) resolveTargetPath(st *session.State, raw string) (string, error) {
	tok := strings.TrimSpace(raw)
	if tok == "" {
		return "", errors.New("empty path")
	}
	if abs, ok := expandPathCandidate(tok); ok {
		return filepath.Clean(abs), nil
	}
	base := h.effectiveCwd(st)
	if base == "" {
		return "", errors.New("no working directory; pass an absolute path or select a project first")
	}
	return filepath.Clean(filepath.Join(base, tok)), nil
}

// findArtifacts walks root recursively and returns regular files matching the
// artifact patterns (glob against the base name, or against the slash relative
// path when the pattern contains a slash), newest first.
func findArtifacts(root string, patterns []string) ([]artifactFile, error) {
	var out []artifactFile
	seen := make(map[string]bool)
	for _, pat := range patterns {
		pat = strings.TrimSpace(pat)
		if pat == "" {
			continue
		}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				return nil
			}
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				rel = d.Name()
			}
			if !matchArtifact(pat, rel) {
				return nil
			}
			if seen[path] {
				return nil
			}
			info, ierr := d.Info()
			if ierr != nil {
				return nil
			}
			seen[path] = true
			out = append(out, artifactFile{
				Path: path,
				Name: d.Name(),
				Rel:  filepath.ToSlash(rel),
				Size: info.Size(),
				Mod:  info.ModTime(),
			})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Mod.Equal(out[j].Mod) {
			return out[i].Name < out[j].Name
		}
		return out[i].Mod.After(out[j].Mod)
	})
	return out, nil
}

// matchArtifact reports whether a relative artifact path matches a pattern.
// Patterns without glob characters act as substring filters.
func matchArtifact(pattern, rel string) bool {
	name := filepath.Base(rel)
	srel := filepath.ToSlash(rel)
	spat := strings.TrimSuffix(filepath.ToSlash(pattern), "/")
	if strings.ContainsAny(spat, "*?[") {
		if ok, _ := filepath.Match(spat, name); ok {
			return true
		}
		if ok, _ := filepath.Match(spat, srel); ok {
			return true
		}
		if !strings.Contains(spat, "/") {
			if ok, _ := filepath.Match(spat, srel); ok {
				return true
			}
		}
		return false
	}
	spat = strings.ToLower(spat)
	return strings.Contains(strings.ToLower(name), spat) || strings.Contains(strings.ToLower(srel), spat)
}

// filterArtifacts narrows a list to entries whose name or relative path
// contains sub (case-insensitive). An empty sub returns the input unchanged.
func filterArtifacts(files []artifactFile, sub string) []artifactFile {
	sub = strings.ToLower(strings.TrimSpace(sub))
	if sub == "" {
		return files
	}
	var out []artifactFile
	for _, f := range files {
		if strings.Contains(strings.ToLower(f.Name), sub) || strings.Contains(strings.ToLower(f.Rel), sub) {
			out = append(out, f)
		}
	}
	return out
}

// zipToTemp zips path into a temporary archive and returns the temp path and
// the resulting zip size.
func zipToTemp(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()

	tmp, err := os.CreateTemp("", "termilink-*.zip")
	if err != nil {
		return "", 0, err
	}
	defer tmp.Close()

	zw := zip.NewWriter(tmp)
	w, err := zw.Create(filepath.Base(path))
	if err != nil {
		_ = os.Remove(tmp.Name())
		return "", 0, err
	}
	if _, err := io.Copy(w, f); err != nil {
		_ = zw.Close()
		_ = os.Remove(tmp.Name())
		return "", 0, err
	}
	if err := zw.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return "", 0, err
	}
	info, err := tmp.Stat()
	if err != nil {
		_ = os.Remove(tmp.Name())
		return "", 0, err
	}
	return tmp.Name(), info.Size(), nil
}

// sendFileWithZipFallback sends path as a document. Files at or under the
// upload cap go as-is; larger files are zipped and retried. The returned note
// describes a zip fallback; a non-nil error means the file could not be
// delivered (e.g. it exceeds the Telegram limit even zipped).
func (h *Handler) sendFileWithZipFallback(ctx context.Context, b *tg.Bot, chatID int64, path, caption string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("not a regular file: %s", path)
	}

	send := func(p, name string) error {
		if b == nil {
			return errors.New("no telegram client")
		}
		f, e := os.Open(p)
		if e != nil {
			return e
		}
		defer f.Close()
		_, e = b.SendDocument(ctx, &tg.SendDocumentParams{
			ChatID:   chatID,
			Document: &models.InputFileUpload{Filename: name, Data: f},
			Caption:  caption,
		})
		return e
	}

	lim := h.uploadLimit()
	if info.Size() <= lim {
		return "", send(path, filepath.Base(path))
	}

	zipPath, zipSize, zerr := zipToTemp(path)
	if zerr != nil {
		return "", fmt.Errorf("zip failed: %w", zerr)
	}
	defer os.Remove(zipPath)
	if zipSize > lim {
		return "", fmt.Errorf("%s: %s (%s zipped) exceeds the %s upload limit",
			filepath.Base(path), humanSize(info.Size()), humanSize(zipSize), humanSize(lim))
	}
	if err := send(zipPath, filepath.Base(path)+".zip"); err != nil {
		return "", err
	}
	return fmt.Sprintf("zipped %s → %s.zip (%s)", filepath.Base(path), filepath.Base(path), humanSize(zipSize)), nil
}

// downloadTGFile fetches a Telegram file download URL with a size limit.
func downloadTGFile(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download failed: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("file exceeds the %s upload limit", humanSize(limit))
	}
	return body, nil
}

// sanitizeUploadFilename reduces a Telegram file name to a safe base name.
func sanitizeUploadFilename(name string) string {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	name = filepath.Base(filepath.Clean(name))
	if name == "" || name == "." || name == ".." || name == "/" {
		return ""
	}
	if strings.Contains(name, "..") || len(name) > 255 {
		return ""
	}
	return name
}

// savedPathNoClobber returns a write path that never overwrites: existing names
// get a -1, -2, … suffix.
func savedPathNoClobber(dir, name string) string {
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 1; i < 1000; i++ {
		cand := filepath.Join(dir, fmt.Sprintf("%s-%d%s", base, i, ext))
		if _, err := os.Stat(cand); os.IsNotExist(err) {
			return cand
		}
	}
	return ""
}
