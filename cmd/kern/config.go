package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// contextConfig holds connection & auth settings for one KERN control plane.
type contextConfig struct {
	Server      string `json:"server"`
	Token       string `json:"token,omitempty"`
	TokenID     string `json:"token_id,omitempty"`
	TokenPrefix string `json:"token_prefix,omitempty"`
	Email       string `json:"email,omitempty"`
	Project     string `json:"project,omitempty"`
}

// configFile is ~/.config/kern/config.json (mode 0600 — it carries tokens).
// Top-level Server/Token/TokenID/Project mirror the active context for
// backward compatibility with older versions and scripts.
// Environment variables KERN_SERVER / KERN_URL, KERN_TOKEN, KERN_PROJECT,
// KERN_CONTEXT override it; command-line flags override everything.
type configFile struct {
	Server         string                   `json:"server,omitempty"`
	Token          string                   `json:"token,omitempty"`
	TokenID        string                   `json:"token_id,omitempty"`
	Project        string                   `json:"project,omitempty"`
	CurrentContext string                   `json:"current_context,omitempty"`
	Contexts       map[string]contextConfig `json:"contexts,omitempty"`
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
	cfg.normalize()
	return cfg
}

// normalize migrates legacy flat configs into the multi-context map and keeps
// top-level fields synced with the active context.
func (cfg *configFile) normalize() {
	if cfg.Contexts == nil {
		cfg.Contexts = map[string]contextConfig{}
	}
	if len(cfg.Contexts) == 0 && (cfg.Server != "" || cfg.Token != "" || cfg.Project != "") {
		name := contextNameForURL(cfg.Server)
		if name == "" {
			name = "default"
		}
		cfg.Contexts[name] = contextConfig{
			Server:  normalizeServerURL(cfg.Server),
			Token:   cfg.Token,
			TokenID: cfg.TokenID,
			Project: cfg.Project,
		}
		cfg.CurrentContext = name
	}
	if cfg.CurrentContext == "" && len(cfg.Contexts) > 0 {
		names := cfg.contextNames()
		cfg.CurrentContext = names[0]
	}
	if cur, ok := cfg.Contexts[cfg.CurrentContext]; ok {
		cfg.Server = cur.Server
		cfg.Token = cur.Token
		cfg.TokenID = cur.TokenID
		cfg.Project = cur.Project
	}
}

func (cfg *configFile) contextNames() []string {
	names := make([]string, 0, len(cfg.Contexts))
	for k := range cfg.Contexts {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

func (cfg *configFile) activeContext(override string) (string, contextConfig, bool) {
	cfg.normalize()
	name := override
	if name == "" {
		name = os.Getenv("KERN_CONTEXT")
	}
	if name == "" {
		name = cfg.CurrentContext
	}
	if name != "" {
		if ctx, ok := cfg.Contexts[name]; ok {
			return name, ctx, true
		}
		if override != "" || os.Getenv("KERN_CONTEXT") != "" {
			return name, contextConfig{}, false
		}
	}
	return "default", contextConfig{
		Server:  cfg.Server,
		Token:   cfg.Token,
		TokenID: cfg.TokenID,
		Project: cfg.Project,
	}, false
}

func (cfg *configFile) upsertContext(name string, cc contextConfig, activate bool) {
	if cfg.Contexts == nil {
		cfg.Contexts = map[string]contextConfig{}
	}
	if name == "" {
		name = contextNameForURL(cc.Server)
	}
	if name == "" {
		name = "default"
	}
	cc.Server = normalizeServerURL(cc.Server)
	cfg.Contexts[name] = cc
	if activate || cfg.CurrentContext == "" || cfg.CurrentContext == name {
		cfg.CurrentContext = name
		cfg.Server = cc.Server
		cfg.Token = cc.Token
		cfg.TokenID = cc.TokenID
		cfg.Project = cc.Project
	}
}

func (cfg *configFile) findContextByServer(server string) string {
	want := normalizeServerURL(server)
	if want == "" {
		return ""
	}
	for _, name := range cfg.contextNames() {
		if normalizeServerURL(cfg.Contexts[name].Server) == want {
			return name
		}
	}
	return ""
}

func saveConfig(cfg configFile) error {
	cfg.normalize()
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

// normalizeServerURL turns bare hosts like "kern.tdvorak.dev" or
// "localhost:8080/api/v1/" into canonical base URLs ("https://kern.tdvorak.dev",
// "http://localhost:8080").
func normalizeServerURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		host := strings.SplitN(s, "/", 2)[0]
		lower := strings.ToLower(host)
		if strings.HasPrefix(lower, "localhost") ||
			strings.HasPrefix(lower, "127.") ||
			strings.HasPrefix(lower, "[::1]") ||
			strings.HasPrefix(lower, "0.0.0.0") ||
			strings.HasSuffix(lower, ".local") ||
			strings.Contains(lower, ".local:") {
			s = "http://" + s
		} else {
			s = "https://" + s
		}
	}
	s = strings.TrimRight(s, "/")
	s = strings.TrimSuffix(s, "/api/v1")
	return strings.TrimRight(s, "/")
}

// contextNameForURL derives a clean profile name from a server URL.
func contextNameForURL(raw string) string {
	norm := normalizeServerURL(raw)
	if norm == "" {
		return ""
	}
	u, err := url.Parse(norm)
	if err != nil || u.Host == "" {
		return ""
	}
	host := u.Hostname()
	if host == "127.0.0.1" || host == "::1" || host == "localhost" {
		if p := u.Port(); p != "" && p != "80" && p != "443" {
			return "local-" + p
		}
		return "local"
	}
	return host
}

// resolve applies flag > env > active-context precedence.
func (c *cli) resolve() error {
	ctxName := first(c.context, os.Getenv("KERN_CONTEXT"))
	_, active, found := c.cfg.activeContext(ctxName)
	if ctxName != "" && !found {
		return fmt.Errorf("unknown context %q — run `kern context list` to see available contexts", ctxName)
	}
	if c.server == "" {
		c.server = first(os.Getenv("KERN_SERVER"), os.Getenv("KERN_URL"))
	}
	if c.server == "" {
		c.server = active.Server
	}
	c.server = normalizeServerURL(c.server)
	if c.token == "" {
		c.token = os.Getenv("KERN_TOKEN")
	}
	if c.token == "" {
		c.token = active.Token
	}
	if c.project == "" {
		c.project = os.Getenv("KERN_PROJECT")
	}
	if c.project == "" {
		c.project = active.Project
	}
	if c.server == "" {
		return fmt.Errorf("no server configured — run `kern auth login` (or pass --url <url> / set KERN_URL)")
	}
	return nil
}

// requireAuth fails fast before a command hits the API unauthenticated.
func (c *cli) requireAuth() error {
	if err := c.resolve(); err != nil {
		return err
	}
	if c.token == "" {
		return fmt.Errorf("not logged in to %s — run `kern auth login` or set KERN_TOKEN", c.server)
	}
	return nil
}
