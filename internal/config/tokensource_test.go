package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The origin of the token is what a service install has to know, because the
// value itself cannot tell it: Load substitutes a ${TELEGRAM_BOT_TOKEN}
// reference, so by the time anyone inspects BotToken a token from the
// environment looks exactly like one from a file.
func TestTokenSourceIsRecorded(t *testing.T) {
	tests := []struct {
		name string
		// body is the config file. ${TELEGRAM_BOT_TOKEN} stays a reference.
		body string
		// dotEnv, when set, is written next to the config file.
		dotEnv string
		// callerEnv is set in the environment before Load runs.
		callerEnv string
		want      string
	}{
		{
			name: "literal in config.yaml is portable",
			body: `
telegram:
  bot_token: literal-token
security:
  allowed_users: [1]
`,
			want: TokenSourceConfig,
		},
		{
			name: "dotenv beside the config file is portable",
			body: `
telegram:
  bot_token: ${TELEGRAM_BOT_TOKEN}
security:
  allowed_users: [1]
`,
			dotEnv: "TELEGRAM_BOT_TOKEN=from-dotenv\n",
			want:   TokenSourceDotEnv,
		},
		{
			name: "the caller's own environment is not portable",
			body: `
telegram:
  bot_token: ${TELEGRAM_BOT_TOKEN}
security:
  allowed_users: [1]
`,
			callerEnv: "from-the-caller",
			want:      TokenSourceProcessEnv,
		},
		{
			name: "a value the caller already had survives .env, and it is the origin",
			// loadDotEnv does not overwrite a variable that is already set, so
			// the caller's value is the one that survives — and the origin must
			// say so, because that is the value the service will not have.
			body: `
telegram:
  bot_token: ${TELEGRAM_BOT_TOKEN}
security:
  allowed_users: [1]
`,
			dotEnv:    "TELEGRAM_BOT_TOKEN=from-dotenv\n",
			callerEnv: "from-the-caller",
			want:      TokenSourceProcessEnv,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Isolate the process environment: a stray TELEGRAM_BOT_TOKEN from
			// the developer's shell would otherwise decide the answer.
			t.Setenv("TELEGRAM_BOT_TOKEN", "")
			if tt.callerEnv != "" {
				t.Setenv("TELEGRAM_BOT_TOKEN", tt.callerEnv)
			}

			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(path, []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if tt.dotEnv != "" {
				if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(tt.dotEnv), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			cfg, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.Telegram.TokenSource; got != tt.want {
				t.Errorf("TokenSource = %q, want %q", got, tt.want)
			}
			if cfg.Telegram.BotToken == "" {
				t.Error("token did not resolve")
			}
		})
	}
}

// The .env belongs to the config file, not to whatever directory the command
// happened to be run from. This is the difference between a service install that
// reads the token and one that refuses to start, so it is worth pinning even
// though it is invisible when both paths are the same.
func TestDotEnvIsReadNextToTheConfigFileNotTheWorkingDirectory(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
telegram:
  bot_token: ${TELEGRAM_BOT_TOKEN}
security:
  allowed_users: [1]
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("TELEGRAM_BOT_TOKEN=beside-the-config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A decoy in the working directory, which must not win.
	elsewhere := t.TempDir()
	if err := os.WriteFile(filepath.Join(elsewhere, ".env"), []byte("TELEGRAM_BOT_TOKEN=in-the-cwd\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(elsewhere); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Telegram.BotToken != "beside-the-config" {
		t.Errorf("token = %q, want the .env beside the config file", cfg.Telegram.BotToken)
	}
}
