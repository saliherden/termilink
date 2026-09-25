package telegram

import (
	"archive/tar"
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

	"github.com/saliherden/termilink/internal/audit"
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

// tarToTemp wraps path into an uncompressed .tar archive inside a temporary
// file and returns its path. Link deliveries use archives so that host
// file-type restrictions (e.g. .apk on uguu.se) don't block the transfer; a
// plain tar adds no compression CPU cost and keeps the same size.
func tarToTemp(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp("", "termilink-*.tar")
	if err != nil {
		return "", err
	}
	tw := tar.NewWriter(tmp)
	if err := tw.WriteHeader(&tar.Header{
		Name:    filepath.Base(path),
		Mode:    0o600,
		Size:    info.Size(),
		ModTime: info.ModTime(),
	}); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return "", err
	}
	if _, err := io.Copy(tw, f); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return "", err
	}
	if err := tw.Close(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}

// deliverFile sends path as a document. Files at or under the upload cap go
// as-is; larger files are zipped and retried. When the archive still exceeds
// Telegram's document limit the file is queued behind an owner approval that
// delivers it through a temporary anonymous link (if telegram.big_file_link_host
// is configured) — otherwise a clear size error is returned. The returned note
// describes a zip fallback or a pending link approval; a non-nil error means the
// file could not be delivered.
func (h *Handler) deliverFile(ctx context.Context, b *tg.Bot, chatID int64, userID int64, path, caption string) (string, error) {
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
	if zipSize <= lim {
		if err := send(zipPath, filepath.Base(path)+".zip"); err != nil {
			return "", err
		}
		return fmt.Sprintf("zipped %s → %s.zip (%s)", filepath.Base(path), filepath.Base(path), humanSize(zipSize)), nil
	}

	if h.bigFileLink == "" {
		return "", fmt.Errorf("%s: %s (%s zipped) exceeds the %s upload limit",
			filepath.Base(path), humanSize(info.Size()), humanSize(zipSize), humanSize(lim))
	}
	tarPath, terr := tarToTemp(path)
	if terr != nil {
		return "", fmt.Errorf("archive failed: %w", terr)
	}
	// The approval request now owns tarPath: it is removed when the owner
	// resolves the request (approve, deny, or timeout).
	if _, err := h.enqueueLinkApproval(ctx, b, chatID, userID, path, tarPath, info.Size()); err != nil {
		_ = os.Remove(tarPath)
		return "", err
	}
	return "", nil
}

// enqueueLinkApproval registers a chat-scoped approval request to deliver a
// file through a temporary anonymous link and asks the owner for confirmation.
// The archive (a temporary .tar) is not uploaded until approved; the original
// path is kept only for display. The caller hands over archive ownership.
func (h *Handler) enqueueLinkApproval(ctx context.Context, b *tg.Bot, chatID int64, userID int64, original, archive string, size int64) (string, error) {
	if h.filesPendingFor(chatID) != nil {
		return "", fmt.Errorf("a link approval is already pending — answer it with *yes* / *no* first, then `get` the file again.")
	}
	req := pendingFile{
		userID:  userID,
		path:    archive,
		display: filepath.Base(original),
		size:    size,
		chatID:  chatID,
		host:    h.bigFileLink,
		expires: time.Now().Add(approvalTTL),
	}
	h.filePendingMu.Lock()
	h.filesPending[chatID] = req
	h.filePendingMu.Unlock()

	e := entryFor(chatID, userID, h.authorizer.IsOwner(userID), audit.ActionApprovalReq, req.display)
	e.Detail = "link " + h.bigFileLink
	h.auditEvent(e)

	h.send(ctx, b, chatID, formatBigFileApprovalPrompt(original, size, h.bigFileLink, approvalTTL))
	return "", nil
}

// filesPendingFor returns the chat's active link-approval request, if any.
func (h *Handler) filesPendingFor(chatID int64) *pendingFile {
	h.filePendingMu.Lock()
	defer h.filePendingMu.Unlock()
	req, ok := h.filesPending[chatID]
	if !ok {
		return nil
	}
	if time.Now().After(req.expires) {
		delete(h.filesPending, chatID)
		_ = os.Remove(req.path)
		return nil
	}
	return &req
}

// resolveFileApproval consumes yes/no answers to a pending link-approval
// request. Only the owner may approve; approval uploads the file to the link
// host and posts the public URL.
func (h *Handler) resolveFileApproval(ctx context.Context, b *tg.Bot, chatID int64, userID int64, text string) {
	h.filePendingMu.Lock()
	req, ok := h.filesPending[chatID]
	if ok {
		delete(h.filesPending, chatID)
	}
	h.filePendingMu.Unlock()
	if !ok || time.Now().After(req.expires) {
		raw := ""
		if ok {
			raw = req.display
			_ = os.Remove(req.path)
		}
		e := entryFor(chatID, userID, h.authorizer.IsOwner(userID), audit.ActionApprovalTimed, raw)
		e.Detail = "link"
		h.auditEvent(e)
		h.sendPlain(ctx, b, chatID, formatLinkApprovalTimeout())
		return
	}
	if !h.authorizer.IsOwner(userID) {
		h.filePendingMu.Lock()
		h.filesPending[chatID] = req
		h.filePendingMu.Unlock()
		e := entryFor(chatID, userID, false, audit.ActionApprovalBlock, req.display)
		e.Detail = "link"
		h.auditEvent(e)
		h.sendPlain(ctx, b, chatID, "🔒 Only the owner can approve link deliveries.")
		return
	}
	switch approvalVerdict(text) {
	case answerApprove:
		e := entryFor(chatID, userID, true, audit.ActionApprovalOK, req.display)
		e.Detail = "link " + req.host
		h.auditEvent(e)
		upCtx, cancel := context.WithTimeout(ctx, linkUploadTimeout)
		defer cancel()
		url, err := linkUpload(upCtx, req.host, req.path)
		_ = os.Remove(req.path)
		if err != nil {
			fe := entryFor(chatID, userID, true, audit.ActionFileLink, req.display)
			fe.OK = audit.Bool(false)
			fe.Err = err.Error()
			fe.Detail = "link " + req.host
			h.auditEvent(fe)
			h.send(ctx, b, chatID, formatErr("link upload failed: "+err.Error()))
			return
		}
		fe := entryFor(chatID, userID, true, audit.ActionFileLink, req.display)
		fe.OK = audit.Bool(true)
		fe.Detail = "link " + req.host
		h.auditEvent(fe)
		h.sendPlain(ctx, b, chatID, fmt.Sprintf(
			"📎 *%s* (%s) → %s\n\n(uploaded as a .tar archive) Retention: %s. The link is public while active.",
			escapeCode(req.display), humanSize(req.size), url,
			linkRetentionNote(req.host)))
	case answerDeny:
		e := entryFor(chatID, userID, true, audit.ActionApprovalNo, req.display)
		e.Detail = "link"
		h.auditEvent(e)
		_ = os.Remove(req.path)
		h.sendPlain(ctx, b, chatID, "❌ Rejected — the file was not sent via a link.")
	default:
		h.filePendingMu.Lock()
		h.filesPending[chatID] = req
		h.filePendingMu.Unlock()
		h.send(ctx, b, chatID, formatLinkApprovalStillPending())
	}
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
