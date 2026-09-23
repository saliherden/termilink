package telegram

import (
	"testing"

	"github.com/saliherden/termilink/internal/config"
	"github.com/saliherden/termilink/internal/session"
)

func testHandler() *Handler {
	return &Handler{
		projects: map[string]config.ProjectConfig{
			"srv": {Path: "/var/opt/srv", Commands: map[string]string{"deploy": "make deploy"}},
			"web": {Path: "/var/www/web", Commands: map[string]string{"build": "make build"}},
		},
	}
}

func TestResolveProjectShortcut(t *testing.T) {
	h := testHandler()
	st := &session.State{Cwd: "/var/opt/srv"}

	got, isCd := h.resolve(st, "deploy")
	if isCd {
		t.Fatal("deploy should not be a cd command")
	}
	if got != "make deploy" {
		t.Fatalf("resolve(deploy) = %q, want %q", got, "make deploy")
	}

	got, _ = h.resolve(st, "nope")
	if got != "nope" {
		t.Fatalf("resolve(unmapped) = %q, want passthrough", got)
	}

	st.Cwd = "/tmp"
	got, _ = h.resolve(st, "deploy")
	if got != "deploy" {
		t.Fatalf("resolve(outside project) = %q, want passthrough", got)
	}
}

func TestResolveProjectShortcutTrailingSlash(t *testing.T) {
	h := testHandler()
	st := &session.State{Cwd: "/var/opt/srv/"}
	got, _ := h.resolve(st, "deploy")
	if got != "make deploy" {
		t.Fatalf("resolve with trailing slash = %q, want %q", got, "make deploy")
	}
	if n := h.projectNameByPath(st.Cwd); n != "srv" {
		t.Fatalf("projectNameByPath = %q, want srv", n)
	}
}

func TestProjectNameByPath(t *testing.T) {
	h := testHandler()
	cases := []struct {
		cwd, want string
	}{
		{"/var/opt/srv", "srv"},
		{"/var/www/web", "web"},
		{"/tmp", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := h.projectNameByPath(tc.cwd); got != tc.want {
			t.Errorf("projectNameByPath(%q) = %q, want %q", tc.cwd, got, tc.want)
		}
	}
}

func TestIsCdCommand(t *testing.T) {
	for _, in := range []string{"cd", "cd /tmp", "cd -", "cd ~"} {
		if !isCdCommand(in) {
			t.Errorf("isCdCommand(%q) = false, want true", in)
		}
	}
	for _, in := range []string{"build", "/cd", "cdx", "cd2 -l"} {
		if isCdCommand(in) {
			t.Errorf("isCdCommand(%q) = true, want false", in)
		}
	}
}