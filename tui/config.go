package tui

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// tuiConfig holds the runtime knobs the TUI cares about. It lives next to
// torii's main config (~/.config/torii/tui.yaml) so the user can edit either
// independently, and so a follow-up release that ships a different default
// listen address keeps the user's overrides intact.
type tuiConfig struct {
	Token   string `yaml:"token"`
	BaseURL string `yaml:"base_url"`
	Model   string `yaml:"model"`
}

// tuiConfigPath returns the canonical path. The directory is created on
// save (loadTUIConfig is read-only). We follow XDG semantics — the daemon
// also writes ~/.config/torii/config.yaml.
func tuiConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "torii", "tui.yaml"), nil
}

// loadTUIConfig reads tui.yaml. Returns (nil, nil) if the file does not
// exist — callers treat that as "needs bootstrap".
func loadTUIConfig() (*tuiConfig, error) {
	p, err := tuiConfigPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var c tuiConfig
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	return &c, nil
}

// saveTUIConfig writes tui.yaml atomically (write-then-rename) so a
// crash mid-write can't corrupt the token file.
func saveTUIConfig(c *tuiConfig) error {
	p, err := tuiConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
