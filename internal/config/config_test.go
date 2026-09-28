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
  big_file_link_host: catbox.moe
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
	if cfg.Telegram.BigFileLinkHost != "catbox.moe" {
		t.Fatalf("big_file_link_host = %q, want catbox.moe", cfg.Telegram.BigFileLinkHost)
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

func TestLoadRejectsBadBigFileLinkHost(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "t")
	path := writeConfig(t, `
telegram:
  bot_token: t
  big_file_link_host: mega.nasa
security:
  allowed_users: [1]
terminal:
  shell: /bin/zsh
`)
	if _, err := Load(path); err == nil {
		t.Fatal("invalid big_file_link_host must be rejected")
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

func TestLoadProjectEnv(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "t")
	path := writeConfig(t, `
telegram:
  bot_token: t
security:
  allowed_users: [1]
terminal:
  shell: /bin/zsh
projects:
  api:
    path: /tmp/api
    env:
      API_PORT: "8080"
      FOO: bar
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	env := cfg.Projects["api"].Env
	if env["API_PORT"] != "8080" || env["FOO"] != "bar" {
		t.Fatalf("project env not parsed: %+v", env)
	}
}

func TestLoadRejectsBadProjectEnv(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "t")
	cases := []struct{ name, env string }{
		{"bad key", "      \"A=B\": x"},
		{"bad key newline", "      \"A\\nB\": x"},
		{"newline value", "      A: \"x\\ny\""},
	}
	for _, c := range cases {
		path := writeConfig(t, `
telegram:
  bot_token: t
security:
  allowed_users: [1]
terminal:
  shell: /bin/zsh
projects:
  api:
    path: /tmp/api
    env:
`+c.env+`
`)
		if _, err := Load(path); err == nil {
			t.Fatalf("%s: expected error, got none", c.name)
		}
	}
}

func TestLoadRejectsBadAuditMaxBytes(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "t")
	path := writeConfig(t, `
telegram:
  bot_token: t
security:
  allowed_users: [1]
  audit_max_bytes: -5
terminal:
  shell: /bin/zsh
`)
	if _, err := Load(path); err == nil {
		t.Fatal("negative audit_max_bytes must be rejected")
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

func TestLoadAgentDefaultsEnabled(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "t")
	path := writeConfig(t, "telegram:\n  bot_token: t\nsecurity:\n  allowed_users: [1]\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Agent.Enabled {
		t.Fatal("agent should default to enabled")
	}
	if cfg.Agent.Command != "" {
		t.Fatalf("command should default empty, got %q", cfg.Agent.Command)
	}
	if cfg.Agent.Screen.Mode != "png" {
		t.Fatalf("agent.screen.mode should default to png, got %q", cfg.Agent.Screen.Mode)
	}
}

func TestLoadAgentSection(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "t")
	path := writeConfig(t, `
telegram:
  bot_token: t
security:
  allowed_users: [1]
agent:
  enabled: false
  command: /custom/bin/opencode
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.Enabled {
		t.Fatal("agent.enabled should be false")
	}
	if cfg.Agent.Command != "/custom/bin/opencode" {
		t.Fatalf("command = %q, want /custom/bin/opencode", cfg.Agent.Command)
	}
}

func TestLoadAgentScreenModeText(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "t")
	path := writeConfig(t, `
telegram:
  bot_token: t
security:
  allowed_users: [1]
agent:
  screen:
    mode: text
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.Screen.Mode != "text" {
		t.Fatalf("mode = %q, want text", cfg.Agent.Screen.Mode)
	}
}

func TestLoadAgentScreenModeInvalid(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "t")
	path := writeConfig(t, `
telegram:
  bot_token: t
security:
  allowed_users: [1]
agent:
  screen:
    mode: gif
`)
	if _, err := Load(path); err == nil {
		t.Fatal("invalid agent.screen.mode should fail validation")
	}
}
