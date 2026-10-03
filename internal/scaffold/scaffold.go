// Package scaffold holds the example configuration and environment files that
// `termilink init` writes out. They are embedded so an installed binary can
// scaffold a working directory without the source repository present.
package scaffold

import (
	_ "embed"
	"os"
	"path/filepath"
)

//go:embed config.example.yaml
var configExample []byte

//go:embed env.example
var envExample []byte

// ConfigExample returns the example config.yaml contents.
func ConfigExample() []byte { return configExample }

// EnvExample returns the example .env contents.
func EnvExample() []byte { return envExample }

// WriteConfig writes the example configuration to path with owner-only
// permissions. The caller is expected to have checked that path is free.
func WriteConfig(path string) error {
	return os.WriteFile(path, configExample, 0o600)
}

// WriteEnvExample writes .env.example into dir if it is not already there, and
// returns its path. When the file already exists it is left untouched and the
// returned path is empty.
func WriteEnvExample(dir string) (string, error) {
	path := filepath.Join(dir, ".env.example")
	if _, err := os.Stat(path); err == nil {
		return "", nil
	}
	if err := os.WriteFile(path, envExample, 0o644); err != nil {
		return "", err
	}
	return path, nil
}
