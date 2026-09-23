package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnv(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	content := "# comment\n" +
		"\n" +
		"TELEGRAM_BOT_TOKEN=\"tok-123\"\n" +
		"EMPTY=\n" +
		"QUOTED='single quoted'\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := loadDotEnv(path); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		key, want string
	}{
		{"TELEGRAM_BOT_TOKEN", "tok-123"},
		{"QUOTED", "single quoted"},
		{"EMPTY", ""},
	}
	for _, tt := range tests {
		if got := os.Getenv(tt.key); got != tt.want {
			t.Errorf("%s = %q, want %q", tt.key, got, tt.want)
		}
	}
}

func TestLoadDotEnvExistingEnvWins(t *testing.T) {
	t.Setenv("ALREADY_SET", "process-value")

	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("ALREADY_SET=file-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := loadDotEnv(path); err != nil {
		t.Fatal(err)
	}

	if got := os.Getenv("ALREADY_SET"); got != "process-value" {
		t.Errorf("ALREADY_SET = %q, want process-value", got)
	}
}

func TestLoadDotEnvMissing(t *testing.T) {
	if err := loadDotEnv(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected error for missing file")
	}
}
