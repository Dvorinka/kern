package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// configFile is ~/.config/kern/config.json (mode 0600 — it carries the token).
// Environment variables KERN_SERVER, KERN_TOKEN, KERN_PROJECT override it;
// command-line flags override everything.
type configFile struct {
	Server  string `json:"server"`
	Token   string `json:"token"`
	TokenID string `json:"token_id,omitempty"`
	Project string `json:"project,omitempty"`
}

func configPath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "kern", "config.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "kern", "config.json")
}

func loadConfig() configFile {
	var cfg configFile
	data, err := os.ReadFile(configPath())
	if err != nil {
		return cfg
	}
	_ = json.Unmarshal(data, &cfg)
	return cfg
}

func saveConfig(cfg configFile) error {
	path := configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

// resolve applies flag > env > config precedence.
func (c *cli) resolve() error {
	if c.server == "" {
		c.server = os.Getenv("KERN_SERVER")
	}
	if c.server == "" {
		c.server = c.cfg.Server
	}
	if c.token == "" {
		c.token = os.Getenv("KERN_TOKEN")
	}
	if c.token == "" {
		c.token = c.cfg.Token
	}
	if c.project == "" {
		c.project = os.Getenv("KERN_PROJECT")
	}
	if c.project == "" {
		c.project = c.cfg.Project
	}
	if c.server == "" {
		return fmt.Errorf("no server configured — `kern login --server <url>` or set KERN_SERVER")
	}
	return nil
}

// requireAuth fails fast before a command hits the API unauthenticated.
func (c *cli) requireAuth() error {
	if err := c.resolve(); err != nil {
		return err
	}
	if c.token == "" {
		return fmt.Errorf("not logged in — `kern login` or set KERN_TOKEN")
	}
	return nil
}
