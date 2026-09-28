package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const DefaultConfigFileName = "config.yaml"

// Duration is a time.Duration that marshals/unmarshals from a YAML string
// such as "30m", "1h30m".
type Duration time.Duration

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) Std() time.Duration { return time.Duration(d) }

type Config struct {
	Telegram  TelegramConfig           `yaml:"telegram"`
	Security  SecurityConfig           `yaml:"security"`
	Workspace WorkspaceConfig          `yaml:"workspace"`
	Terminal  TerminalConfig           `yaml:"terminal"`
	Agent     AgentConfig              `yaml:"agent"`
	Projects  map[string]ProjectConfig `yaml:"projects"`

	// Path of the loaded configuration file. Not serialized.
	Path string `yaml:"-"`
}

type TelegramConfig struct {
	BotToken string `yaml:"bot_token"`
	// MaxFileBytes caps file transfers (get / auto-upload). Default: 50 MiB,
	// Telegram's document upload limit.
	MaxFileBytes int64 `yaml:"max_file_bytes"`
	// BigFileLinkHost enables delivering files that are too big for Telegram
	// through a temporary anonymous link (host upload + link message). Values:
	// "" (disabled, default), "uguu.se" (auto-deletes after ~3 hours),
	// "catbox.moe" (up to 200 MB, persists until purged).
	BigFileLinkHost string `yaml:"big_file_link_host"`
}

type SecurityConfig struct {
	Owner        int64   `yaml:"owner"`
	AllowedUsers []int64 `yaml:"allowed_users"`
	// ApproveDangerous gates destructive commands behind chat approval.
	// Values: "all" (default), "worker", "off".
	ApproveDangerous  string   `yaml:"approve_dangerous"`
	DangerousPatterns []string `yaml:"dangerous_patterns,omitempty"`
	// AuditLog writes a JSONL audit trail of security-sensitive events.
	// Empty uses the default (~/.termilink/audit.log); "off" disables it.
	AuditLog string `yaml:"audit_log"`
	// AuditMaxBytes caps the audit log size in bytes; when exceeded the file
	// is rotated to "<path>.1" before the next entry. 0 = unlimited.
	AuditMaxBytes int64 `yaml:"audit_max_bytes"`
}

type WorkspaceConfig struct {
	Allowed []string `yaml:"allowed"`
}

type TerminalConfig struct {
	Shell          string   `yaml:"shell"`
	CommandTimeout Duration `yaml:"command_timeout"`
	MaxOutputBytes int      `yaml:"max_output_bytes"`
}

// AgentConfig controls the interactive agent (TUI bridge) feature. The agent
// CLI (e.g. opencode) is started in a project directory inside a PTY and its
// screen is relayed to Telegram; only the owner can control it.
type AgentConfig struct {
	// Enabled gates the whole feature (default true).
	Enabled bool `yaml:"enabled"`
	// Command selects the CLI agent binary: "" auto-detects (opencode, claude,
	// codex, gemini via PATH), or set a bare name or an absolute path.
	Command string `yaml:"command"`
	// Screen controls how the agent screen is relayed (default png).
	Screen AgentScreenConfig `yaml:"screen"`
}

// AgentScreenConfig configures the TUI screen relay.
type AgentScreenConfig struct {
	// Mode selects the relay format: "png" sends the screen as a colored image,
	// "text" sends it as a code block.
	//
	// In png mode the live screen is uploaded as a photo. The screen posted when
	// a session ends is always text, whatever this says: it is written output
	// rather than a picture of one, so it stays copyable and searchable. This
	// field governs the live screen only. "text" drops the images altogether, at
	// the cost of colors and layout.
	Mode string `yaml:"mode"`
}

type ProjectConfig struct {
	Path      string            `yaml:"path"`
	Commands  map[string]string `yaml:"commands"`
	Artifacts []string          `yaml:"artifacts"`
	// Env sets additional environment variables (KEY=value) exported into the
	// project's shell session.
	Env map[string]string `yaml:"env"`
}

func Defaults() *Config {
	return &Config{
		Telegram: TelegramConfig{
			MaxFileBytes: 50 << 20,
		},
		Terminal: TerminalConfig{
			Shell:          defaultShell(),
			CommandTimeout: Duration(30 * time.Minute),
			MaxOutputBytes: 1 << 20, // 1 MiB
		},
		Agent: AgentConfig{
			Enabled: true,
			Screen:  AgentScreenConfig{Mode: "png"},
		},
		Projects: map[string]ProjectConfig{},
	}
}

// Load reads and validates the configuration from the given path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}

	if err := loadDotEnv(".env"); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("load .env: %w", err)
	}

	cfg := Defaults()
	cfg.Path = path
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) Validate() error {
	if err := c.resolveToken(); err != nil {
		return err
	}
	if len(c.Security.AllowedUsers) == 0 {
		return fmt.Errorf("security.allowed_users must contain at least one Telegram user id")
	}
	c.resolveOwner()
	switch c.Security.ApproveDangerous {
	case "", "all", "worker", "off":
	default:
		return fmt.Errorf("security.approve_dangerous must be one of all, worker, off (got %q)", c.Security.ApproveDangerous)
	}
	switch c.Agent.Screen.Mode {
	case "", "png", "text":
	default:
		return fmt.Errorf("agent.screen.mode must be one of png, text (got %q)", c.Agent.Screen.Mode)
	}
	if c.Terminal.Shell == "" {
		return fmt.Errorf("terminal.shell must not be empty")
	}
	if c.Telegram.MaxFileBytes <= 0 {
		return fmt.Errorf("telegram.max_file_bytes must be a positive number of bytes")
	}
	switch c.Telegram.BigFileLinkHost {
	case "", "uguu.se", "catbox.moe":
	default:
		return fmt.Errorf("telegram.big_file_link_host must be one of uguu.se, catbox.moe (got %q)", c.Telegram.BigFileLinkHost)
	}
	if c.Security.AuditMaxBytes < 0 {
		return fmt.Errorf("security.audit_max_bytes must be >= 0 (0 = unlimited)")
	}
	for name, p := range c.Projects {
		for k, v := range p.Env {
			if k == "" || strings.ContainsAny(k, "=\n") {
				return fmt.Errorf("projects.%s.env: invalid key %q", name, k)
			}
			if strings.ContainsAny(v, "\n\x00") {
				return fmt.Errorf("projects.%s.env: value for %q contains a newline", name, k)
			}
		}
	}
	return nil
}

func (c *Config) resolveOwner() {
	if c.Security.Owner == 0 {
		c.Security.Owner = c.Security.AllowedUsers[0]
		return
	}
	for _, id := range c.Security.AllowedUsers {
		if id == c.Security.Owner {
			return
		}
	}
	c.Security.AllowedUsers = append(c.Security.AllowedUsers, c.Security.Owner)
}

func (c *Config) resolveToken() error {
	tok := c.Telegram.BotToken
	if tok == "" || strings.HasPrefix(tok, "${") {
		env := os.Getenv("TELEGRAM_BOT_TOKEN")
		if env == "" {
			return fmt.Errorf("telegram.bot_token is empty and TELEGRAM_BOT_TOKEN is not set")
		}
		c.Telegram.BotToken = env
	}
	return nil
}

func defaultShell() string {
	for _, s := range []string{"/bin/zsh", "/bin/bash", "/bin/sh"} {
		if _, err := os.Stat(s); err == nil {
			return s
		}
	}
	return "/bin/sh"
}
