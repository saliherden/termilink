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
	Telegram  TelegramConfig  `yaml:"telegram"`
	Security  SecurityConfig  `yaml:"security"`
	Workspace WorkspaceConfig `yaml:"workspace"`
	Terminal  TerminalConfig  `yaml:"terminal"`
	Projects  map[string]ProjectConfig `yaml:"projects"`

	// Path of the loaded configuration file. Not serialized.
	Path string `yaml:"-"`
}

type TelegramConfig struct {
	BotToken string `yaml:"bot_token"`
}

type SecurityConfig struct {
	Owner        int64   `yaml:"owner"`
	AllowedUsers []int64 `yaml:"allowed_users"`
}

type WorkspaceConfig struct {
	Allowed []string `yaml:"allowed"`
}

type TerminalConfig struct {
	Shell          string   `yaml:"shell"`
	CommandTimeout Duration `yaml:"command_timeout"`
	MaxOutputBytes int      `yaml:"max_output_bytes"`
}

type ProjectConfig struct {
	Path      string            `yaml:"path"`
	Commands  map[string]string `yaml:"commands"`
	Artifacts []string          `yaml:"artifacts"`
}

func Defaults() *Config {
	return &Config{
		Terminal: TerminalConfig{
			Shell:          defaultShell(),
			CommandTimeout: Duration(30 * time.Minute),
			MaxOutputBytes: 1 << 20, // 1 MiB
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
	if c.Terminal.Shell == "" {
		return fmt.Errorf("terminal.shell must not be empty")
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