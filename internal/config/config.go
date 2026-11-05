// Package config loads runtime configuration from the environment.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the full control-plane configuration.
type Config struct {
	Env              string
	HTTPAddr         string
	DatabaseURL      string
	BlobDir          string
	SessionSecret    string
	SecretKey        []byte
	BootstrapEmail   string
	BootstrapPass    string
	BootstrapOrg     string
	PublicBaseURL    string
	GitHub           GitHubConfig
	RunnerJoinToken  string
	RunnerTokenTTL   time.Duration
	SessionTTL       time.Duration
	SchedulerTick    time.Duration
	JobLease         time.Duration
	MaxJobAttempts   int
	LogRetention     time.Duration
	CacheMaxAge      time.Duration
	WebhookRetention time.Duration
	AllowedOrigins   []string
	DefaultImage     string
	WebDir           string
}

type GitHubConfig struct {
	AppID         string
	PrivateKeyPEM string
	WebhookSecret string
	APIBaseURL    string
	// StatusToken is a PAT used for the token-based (M4-lite) integration:
	// commit statuses and, for private repos, workflow discovery.
	StatusToken string
}

// Load reads configuration and validates the required values.
func Load() (*Config, error) {
	cfg := &Config{
		Env:            env("KERN_ENV", "development"),
		HTTPAddr:       env("KERN_HTTP_ADDR", ":8080"),
		DatabaseURL:    env("KERN_DATABASE_URL", "postgres://kern:kern@localhost:5432/kern?sslmode=disable"),
		BlobDir:        env("KERN_BLOB_DIR", "/var/lib/kern/blobs"),
		SessionSecret:  env("KERN_SESSION_SECRET", ""),
		SecretKey:      []byte(env("KERN_SECRET_KEY", "")),
		BootstrapEmail: env("KERN_BOOTSTRAP_EMAIL", ""),
		BootstrapPass:  env("KERN_BOOTSTRAP_PASSWORD", ""),
		BootstrapOrg:   env("KERN_BOOTSTRAP_ORG", "kern"),
		PublicBaseURL:  strings.TrimRight(env("KERN_PUBLIC_URL", "http://localhost:8080"), "/"),
		GitHub: GitHubConfig{
			AppID:         env("KERN_GITHUB_APP_ID", ""),
			PrivateKeyPEM: strings.ReplaceAll(env("KERN_GITHUB_APP_PRIVATE_KEY", ""), `\n`, "\n"),
			WebhookSecret: env("KERN_GITHUB_WEBHOOK_SECRET", ""),
			APIBaseURL:    env("KERN_GITHUB_API_URL", "https://api.github.com"),
			StatusToken:   env("KERN_GITHUB_TOKEN", ""),
		},
		RunnerJoinToken:  env("KERN_RUNNER_JOIN_TOKEN", "kern-runner-join"),
		RunnerTokenTTL:   duration("KERN_RUNNER_TOKEN_TTL", 24*time.Hour),
		SessionTTL:       duration("KERN_SESSION_TTL", 14*24*time.Hour),
		SchedulerTick:    duration("KERN_SCHEDULER_TICK", 2*time.Second),
		JobLease:         duration("KERN_JOB_LEASE", 90*time.Second),
		MaxJobAttempts:   integer("KERN_MAX_JOB_ATTEMPTS", 3),
		LogRetention:     duration("KERN_LOG_RETENTION", 30*24*time.Hour),
		CacheMaxAge:      duration("KERN_CACHE_MAX_AGE", 7*24*time.Hour),
		WebhookRetention: duration("KERN_WEBHOOK_RETENTION", 30*24*time.Hour),
		AllowedOrigins:   splitList(env("KERN_ALLOWED_ORIGINS", "http://localhost:5173,http://localhost:8081")),
		DefaultImage:     env("KERN_DEFAULT_IMAGE", "ubuntu:24.04"),
		WebDir:           env("KERN_WEB_DIR", ""),
	}

	if f := env("KERN_GITHUB_APP_PRIVATE_KEY_FILE", ""); f != "" {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("KERN_GITHUB_APP_PRIVATE_KEY_FILE: %w", err)
		}
		cfg.GitHub.PrivateKeyPEM = string(data)
	}
	if len(cfg.SessionSecret) < 32 {
		if cfg.Env == "development" {
			cfg.SessionSecret = "dev-only-session-secret-please-change-0123456789"
		} else {
			return nil, fmt.Errorf("KERN_SESSION_SECRET must be at least 32 characters")
		}
	}
	if len(cfg.SecretKey) != 32 {
		if cfg.Env == "development" {
			// Deterministic development key; production requires a real 32-byte key.
			cfg.SecretKey = []byte("kern-development-secret-key-0001")
		} else {
			return nil, fmt.Errorf("KERN_SECRET_KEY must be exactly 32 bytes (AES-256)")
		}
	}
	if cfg.BootstrapEmail == "" {
		cfg.BootstrapEmail = "admin@kern.local"
	}
	if cfg.BootstrapPass == "" {
		cfg.BootstrapPass = "kern-admin-password"
	}
	return cfg, nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func duration(key string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func integer(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func splitList(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
