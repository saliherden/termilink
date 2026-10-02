package cli

import (
	"bytes"
	"os"
	"testing"

	"github.com/saliherden/termilink/internal/config"
)

// This warning existed and never fired. It looked for the redaction asterisks
// that only `termilink config` prints, which meant the condition matched nothing
// a real install could produce — and the fix for that initially switched on the
// token's value while the cases held origins, which is the same mistake with a
// new coat of paint. Both are invisible from the outside: a warning that does
// not appear looks identical to a configuration that does not need one.
func TestWarnIfTokenNotPortable(t *testing.T) {
	tests := []struct {
		name    string
		source  string
		token   string
		wantHas string
	}{
		{
			name:    "a token only in the environment is the case that matters",
			source:  config.TokenSourceProcessEnv,
			token:   "123456:abcdef",
			wantHas: "does not inherit",
		},
		{
			name:    "a .env next to config.yaml works, and is worth confirming",
			source:  config.TokenSourceDotEnv,
			token:   "123456:abcdef",
			wantHas: "which the service can read",
		},
		{
			name:    "an unknown origin is called out rather than assumed fine",
			source:  "",
			token:   "",
			wantHas: "no bot token resolved",
		},
		{
			name:    "a literal in config.yaml is portable and needs no comment",
			source:  config.TokenSourceConfig,
			token:   "123456:abcdef",
			wantHas: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := captureStderr(t, func() {
				warnIfTokenNotPortable(&config.Config{
					Telegram: config.TelegramConfig{BotToken: tt.token, TokenSource: tt.source},
				})
			})
			if tt.wantHas == "" {
				if out != "" {
					t.Errorf("printed %q, want nothing for a portable token", out)
				}
				return
			}
			if !bytes.Contains([]byte(out), []byte(tt.wantHas)) {
				t.Errorf("output %q does not mention %q", out, tt.wantHas)
			}
		})
	}
}

// A real token whose value happens to equal an origin string must not be
// mistaken for one that came from there. This is the specific shape of the bug:
// switching on the wrong field, so a coincidental value decides the outcome.
func TestWarnIgnoresATokenThatLooksLikeAnOrigin(t *testing.T) {
	out := captureStderr(t, func() {
		warnIfTokenNotPortable(&config.Config{
			Telegram: config.TelegramConfig{
				BotToken:    config.TokenSourceProcessEnv,
				TokenSource: config.TokenSourceConfig,
			},
		})
	})
	if out != "" {
		t.Errorf("a config.yaml token whose value is an origin string printed %q", out)
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r)
		done <- buf.String()
	}()
	fn()
	_ = w.Close()
	os.Stderr = orig
	out := <-done
	_ = r.Close()
	return out
}
