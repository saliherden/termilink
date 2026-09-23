package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadValid(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "env-token")
	path := writeConfig(t, `
telegram:
  bot_token: ${TELEGRAM_BOT_TOKEN}
security:
  allowed_users: [1, 2]
terminal:
  shell: /bin/zsh
  command_timeout: 5m
projects:
  api:
    path: /tmp/api
    commands:
      dev: npm run dev
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Telegram.BotToken != "env-token" {
		t.Fatalf("token = %q, want env-token", cfg.Telegram.BotToken)
	}
	if len(cfg.Security.AllowedUsers) != 2 {
		t.Fatalf("allowed_users = %d, want 2", len(cfg.Security.AllowedUsers))
	}
	if cfg.Terminal.CommandTimeout.Std() != 5*time.Minute {
		t.Fatalf("timeout = %s, want 5m", cfg.Terminal.CommandTimeout.Std())
	}
	if p := cfg.Projects["api"]; p.Path != "/tmp/api" || p.Commands["dev"] != "npm run dev" {
		t.Fatalf("project not parsed: %+v", p)
	}
}

func TestLoadRejectsEmptyUsers(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "t")
	path := writeConfig(t, `
telegram:
  bot_token: ${TELEGRAM_BOT_TOKEN}
security:
  allowed_users: []
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for empty allowed_users")
	}
}

func TestLoadRejectsMissingToken(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	path := writeConfig(t, `
telegram:
  bot_token: ""
security:
  allowed_users: [1]
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for missing token")
	}
}

func TestLoadOwnerDefault(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "t")
	path := writeConfig(t, `
telegram:
  bot_token: ${TELEGRAM_BOT_TOKEN}
security:
  allowed_users: [11, 22]
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Security.Owner != 11 {
		t.Fatalf("owner = %d, want 11 (first allowed user)", cfg.Security.Owner)
	}
}

func TestLoadOwnerAutoAppend(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "t")
	path := writeConfig(t, `
telegram:
  bot_token: ${TELEGRAM_BOT_TOKEN}
security:
  owner: 99
  allowed_users: [11]
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Security.Owner != 99 {
		t.Fatalf("owner = %d, want 99", cfg.Security.Owner)
	}
	found := false
	for _, id := range cfg.Security.AllowedUsers {
		if id == 99 {
			found = true
		}
	}
	if !found {
		t.Fatal("owner should be auto-appended to allowed_users")
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load("/no/such/config.yaml"); err == nil {
		t.Fatal("expected error for missing file")
	}
}
