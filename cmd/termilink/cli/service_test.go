package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The dscl answer is parsed rather than trusted whole: it is a single line whose
// value follows "UserShell: ", and taking the line as-is would exec a string that
// is not a path. getent's answer is seven colon-separated fields and is parsed
// the same way.
func TestLastField(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "dscl answer",
			in:   "UserShell: /bin/zsh",
			want: "/bin/zsh",
		},
		{
			name: "dscl answer with leading whitespace",
			in:   "UserShell:    /opt/homebrew/bin/fish",
			want: "/opt/homebrew/bin/fish",
		},
		{
			name: "no separator falls back to the last token",
			in:   "/bin/bash\n",
			want: "/bin/bash",
		},
		{
			name: "empty answer",
			in:   "  ",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lastField(tt.in); got != tt.want {
				t.Errorf("lastField(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// The report has to say which shell it asked, because /bin/sh exits zero while
// returning a PATH with no toolchain on it. A reader who is told nothing would
// take "login shell" at face value.
func TestLoginShellExplainsItself(t *testing.T) {
	shell, how := loginShell()
	if shell == "" {
		t.Fatal("loginShell returned no shell")
	}
	if how == "" {
		t.Error("loginShell named no explanation, so the report cannot say where the answer came from")
	}
}

// The install target is a copy of the running binary under the home directory,
// not the source tree. A relative or source-tree path is the thing the TCC bug
// and the moved-repository bug both came from.
func TestDefaultInstallDirFollowsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if got, want := defaultInstallDir(), filepath.Join(home, ".local", "bin"); got != want {
		t.Errorf("defaultInstallDir() = %q, want %q", got, want)
	}
}

// install copies the running binary byte for byte, because the Mach-O code
// signature is embedded in the file and a re-signed or re-linked copy would lose
// the TCC grant the original earned.
func TestInstallBinaryCopiesTheRunningBinary(t *testing.T) {
	src, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if resolved, err := filepath.EvalSymlinks(src); err == nil {
		src = resolved
	}
	dest := filepath.Join(t.TempDir(), "bin", "termilink")

	if err := installBinary(dest); err != nil {
		t.Fatalf("installBinary(%q) = %v", dest, err)
	}
	want, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Error("the installed binary is not a byte-for-byte copy of the running one")
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("installed binary is not executable: %v", info.Mode())
	}
}

// Reinstalling from the installed binary must not try to copy it onto itself,
// which would truncate the file mid-read on some systems.
func TestInstallBinaryOntoItselfIsANoOp(t *testing.T) {
	src, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if resolved, err := filepath.EvalSymlinks(src); err == nil {
		src = resolved
	}
	if err := installBinary(src); err != nil {
		t.Errorf("installBinary on its own path = %v, want a no-op", err)
	}
}
