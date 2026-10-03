package telegram

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivacyProtectedDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	desktop := filepath.Join(home, "Desktop")
	inside := map[string]bool{
		desktop:                               true,
		filepath.Join(desktop, "Go", "repo"):  true,
		filepath.Join(home, "Documents", "a"): true,
		filepath.Join(home, "Downloads"):      true,
		home:                                  false,
		filepath.Join(home, "code"):           false,
		filepath.Join(home, "DesktopX"):       false,
		"/tmp":                                false,
		"Desktop":                             false,
	}
	for dir, want := range inside {
		if got := privacyProtectedDir(true, dir); got != want {
			t.Errorf("privacyProtectedDir(true, %q) = %v, want %v", dir, got, want)
		}
		if privacyProtectedDir(false, dir) {
			t.Errorf("privacyProtectedDir(false, %q) = true, want false", dir)
		}
	}
}

func TestPrivacyHint(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	desktop := filepath.Join(home, "Desktop", "repo")
	eperm := []byte("ls: .: Operation not permitted\n")

	tests := []struct {
		name   string
		darwin bool
		cwd    string
		out    []byte
		want   bool
	}{
		{"guarded folder and EPERM", true, desktop, eperm, true},
		{"EPERM outside a guarded folder", true, filepath.Join(home, "code"), eperm, false},
		{"guarded folder without EPERM", true, desktop, []byte("hello\n"), false},
		{"not darwin", false, desktop, eperm, false},
		{"relative cwd", true, ".", eperm, false},
	}
	for _, tt := range tests {
		got := privacyHint(tt.darwin, tt.cwd, tt.out)
		if (got != "") != tt.want {
			t.Errorf("%s: privacyHint = %q, want hint=%v", tt.name, got, tt.want)
		}
		if tt.want && !strings.Contains(got, "Full Disk Access") {
			t.Errorf("%s: hint does not mention the remedy: %q", tt.name, got)
		}
	}
}
