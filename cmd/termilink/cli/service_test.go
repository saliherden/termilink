package cli

import "testing"

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
