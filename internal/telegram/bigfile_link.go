package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// linkHostBase maps a configured host name to its API endpoint. Kept as a
// variable so tests can point at a local httptest server.
var linkHostBase = map[string]string{
	"uguu.se":    "https://uguu.se/upload.php",
	"catbox.moe": "https://catbox.moe/user/api.php",
}

// linkUpload is an indirection over uploadToLinkHost so unit tests can stub
// the actual network call.
var linkUpload = uploadToLinkHost

const linkUploadTimeout = 3 * time.Minute

// linkHost describes how to talk to one anonymous transient host.
type linkHost struct {
	field     string            // multipart field that carries the file
	extra     map[string]string // extra fixed multipart fields (written first)
	ua        string            // user agent override; empty uses the default
	retention string            // human note shown in prompts, no trailing dot
	parse     func([]byte) (string, error)
}

// linkHosts details the hosts accepted by telegram.big_file_link_host. Files
// are delivered through the anonymous ephemeral host after owner approval.
var linkHosts = map[string]linkHost{
	"uguu.se": {
		field:     "files[]",
		retention: "≈3 saat sonra otomatik silinir",
		parse:     parseUguuResponse,
	},
	"catbox.moe": {
		field:     "fileToUpload",
		extra:     map[string]string{"reqtype": "fileupload"},
		ua:        "curl/8.5.0",
		retention: "not auto-deleted (may be purged after long inactivity)",
		parse:     parsePlainURL,
	},
}

// uploadToLinkHost pushes path to the anonymous transient host and returns the
// public (single download / time-limited) URL. The file is streamed via
// multipart/form-data to avoid loading it into memory.
func uploadToLinkHost(ctx context.Context, host, path string) (string, error) {
	base, ok := linkHostBase[host]
	if !ok {
		return "", fmt.Errorf("unknown link host: %s", host)
	}
	spec, ok := linkHosts[host]
	if !ok {
		return "", fmt.Errorf("unknown link host: %s", host)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		var werr error
		defer pw.Close()
		for k, v := range spec.extra {
			if err := mw.WriteField(k, v); err != nil {
				werr = err
				break
			}
		}
		if werr == nil {
			var h io.Writer
			if h, werr = mw.CreateFormFile(spec.field, filepath.Base(path)); werr == nil {
				_, werr = io.Copy(h, f)
			}
		}
		if cerr := mw.Close(); cerr != nil && werr == nil {
			werr = cerr
		}
		if werr != nil {
			pw.CloseWithError(werr)
		}
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base, pr)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	ua := "termilink/1.0"
	if spec.ua != "" {
		ua = spec.ua
	}
	req.Header.Set("User-Agent", ua)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s returned HTTP %d: %s", host, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return spec.parse(raw)
}

// parseUguuResponse extracts the url of the first uploaded file. uguu.se
// escapes slashes in its JSON (`\/`), which encoding/json unescapes for us.
func parseUguuResponse(raw []byte) (string, error) {
	s := strings.TrimSpace(string(raw))
	if idx := strings.Index(s, "{"); idx >= 0 {
		s = s[idx:]
	}
	var v struct {
		Success bool `json:"success"`
		Files   []struct {
			URL string `json:"url"`
		} `json:"files"`
		Errors []string `json:"errors"`
	}
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return "", fmt.Errorf("invalid uguu.se response: %w", err)
	}
	if !v.Success {
		if len(v.Errors) > 0 {
			return "", fmt.Errorf("uguu.se upload failed: %s", strings.Join(v.Errors, "; "))
		}
		return "", fmt.Errorf("uguu.se upload reported failure")
	}
	if len(v.Files) == 0 || v.Files[0].URL == "" {
		return "", fmt.Errorf("uguu.se response contained no link")
	}
	return v.Files[0].URL, nil
}

// parsePlainURL treats the response body itself as the download link (catbox).
func parsePlainURL(raw []byte) (string, error) {
	u := strings.TrimSpace(string(raw))
	if u == "" {
		return "", fmt.Errorf("link host returned an empty link")
	}
	return u, nil
}

// linkRetentionNote describes what happens to the uploaded file afterwards.
func linkRetentionNote(host string) string {
	if spec, ok := linkHosts[host]; ok {
		return spec.retention
	}
	return "kept for a limited time"
}

// formatBigFileApprovalPrompt explains why a file must leave Telegram and asks
// for owner approval to deliver it via an anonymous link.
func formatBigFileApprovalPrompt(path string, size int64, host string, ttl time.Duration) string {
	return fmt.Sprintf(
		"⚠️ File exceeds Telegram's 50MB sending limit:\n`%s` (%s)\n\nSend it via a temporary link on %s?\nRetention: %s. The link is public while active (unguessable URL). Only the owner can approve.\n\nReply with *yes* / *evet* / *ok* to approve or *no* / *hayır* to reject (%s).",
		escapeCode(path), humanSize(size), host, linkRetentionNote(host), ttl.Round(time.Second))
}

func formatLinkApprovalStillPending() string {
	return "⏳ A link approval is pending. Reply with *yes* or *no*."
}

func formatLinkApprovalTimeout() string {
	return "⏰ Link approval expired — the file was not sent. Run `get` again if you still want it."
}
