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
	Env             string
	HTTPAddr        string
	DatabaseURL     string
	BlobDir         string
	SessionSecret   string
	SecretKey       []byte
	BootstrapEmail  string
	BootstrapPass   string
	BootstrapOrg    string
	PublicBaseURL   string
	GitHub          GitHubConfig
	RunnerJoinToken string
	RunnerTokenTTL  time.Duration
	SessionTTL      time.Duration
	SchedulerTick   time.Duration
	JobLease        time.Duration
	MaxJobAttempts  int
	LogRetention    time.Duration
	CacheMaxAge     time.Duration
	// CacheMaxBytes caps a single actions/cache archive (KERN_CACHE_MAX_BYTES),
	// enforced at save time; oversized uploads are refused. The default
	// matches GitHub's own per-entry ceiling — a 1 GiB cap rejected real
	// rust-cache saves (#290). 0 disables the cap.
	CacheMaxBytes    int64
	WebhookRetention time.Duration
	AuditRetention   time.Duration
	NotifyRetention  time.Duration
	AllowedOrigins   []string
	DefaultImage     string
	// Arm64Image substitutes DefaultImage for jobs claimed by arm64 runners
	// (KERN_ARM64_IMAGE). Empty disables substitution.
	Arm64Image string
	WebDir     string
	// ReleaseRepo is the "owner/name" slug checked for newer releases on the
	// settings page. Empty disables nothing — the endpoint just falls back.
	ReleaseRepo string
	// GeoIPURL is the lookup template resolving runner egress IPs to coarse
	// locations ("{ip}" placeholder; empty ip queries the control plane's own
	// egress). "off" or "none" disables GeoIP entirely.
	GeoIPURL string
	// BlobS3, when Bucket is set, selects the S3/MinIO blob driver instead of
	// the filesystem. AccessKey/SecretKey may stay empty to use the ambient
	// AWS credential chain (env, IAM).
	BlobS3 struct {
		Endpoint  string
		Region    string
		Bucket    string
		Prefix    string
		AccessKey string
		SecretKey string
		Secure    bool
		PathStyle bool
	}
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
		CacheMaxBytes:    int64(integer("KERN_CACHE_MAX_BYTES", 10<<30)),
		WebhookRetention: duration("KERN_WEBHOOK_RETENTION", 30*24*time.Hour),
		AuditRetention:   duration("KERN_AUDIT_RETENTION", 365*24*time.Hour),
		NotifyRetention:  duration("KERN_NOTIFY_RETENTION", 30*24*time.Hour),
		AllowedOrigins:   splitList(env("KERN_ALLOWED_ORIGINS", "http://localhost:5173,http://localhost:8081")),
		DefaultImage:     env("KERN_DEFAULT_IMAGE", "ubuntu:24.04"),
		Arm64Image:       env("KERN_ARM64_IMAGE", ""),
		WebDir:           env("KERN_WEB_DIR", ""),
		ReleaseRepo:      env("KERN_RELEASE_REPO", "Dvorinka/kern"),
		GeoIPURL:         env("KERN_GEOIP_URL", "https://ipwho.is/{ip}"),
	}
	cfg.BlobS3.Endpoint = env("KERN_BLOB_S3_ENDPOINT", "")
	cfg.BlobS3.Region = env("KERN_BLOB_S3_REGION", "us-east-1")
	cfg.BlobS3.Bucket = env("KERN_BLOB_S3_BUCKET", "")
	cfg.BlobS3.Prefix = env("KERN_BLOB_S3_PREFIX", "")
	cfg.BlobS3.AccessKey = env("KERN_BLOB_S3_ACCESS_KEY", "")
	cfg.BlobS3.SecretKey = env("KERN_BLOB_S3_SECRET_KEY", "")
	cfg.BlobS3.Secure = env("KERN_BLOB_S3_SECURE", "true") != "false"
	// Path-style is required by MinIO and most S3-compatible services; AWS
	// virtual-hosted style is implied when the endpoint is left at AWS.
	cfg.BlobS3.PathStyle = env("KERN_BLOB_S3_PATH_STYLE",
		strconv.FormatBool(cfg.BlobS3.Endpoint != "" && !strings.HasSuffix(cfg.BlobS3.Endpoint, "amazonaws.com"))) == "true"

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
