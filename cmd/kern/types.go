package main

import (
	"encoding/json"
	"time"
)

// API response shapes, mirroring the web client's contract. The CLI declares
// its own copies rather than importing internal/store so the binary stays free
// of the pgx dependency chain.

type user struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
	Role  string `json:"role"`
	OrgID string `json:"org_id"`
}

type apiToken struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Scopes     []string   `json:"scopes"`
	LastUsedAt *time.Time `json:"last_used_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

type project struct {
	ID            string    `json:"id"`
	Slug          string    `json:"slug"`
	Name          string    `json:"name"`
	RepoURL       string    `json:"repo_url"`
	RepoFullName  string    `json:"repo_full_name"`
	LocalPath     string    `json:"local_path"`
	DefaultBranch string    `json:"default_branch"`
	CreatedAt     time.Time `json:"created_at"`
}

type workflow struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	Name string `json:"name"`
}

type run struct {
	ID             string     `json:"id"`
	RunNumber      int        `json:"run_number"`
	Event          string     `json:"event"`
	Ref            string     `json:"ref"`
	SHA            string     `json:"sha"`
	Actor          string     `json:"actor"`
	Status         string     `json:"status"`
	Conclusion     string     `json:"conclusion"`
	TrustLevel     string     `json:"trust_level"`
	WorkflowName   string     `json:"workflow_name"`
	WorkflowPath   string     `json:"workflow_path"`
	ProjectSlug    string     `json:"project_slug"`
	StartedAt      *time.Time `json:"started_at"`
	CompletedAt    *time.Time `json:"completed_at"`
	CreatedAt      time.Time  `json:"created_at"`
	Classification string     `json:"classification,omitempty"`
}

type step struct {
	ID         string          `json:"id"`
	Number     int             `json:"number"`
	Name       string          `json:"name"`
	Status     string          `json:"status"`
	Conclusion string          `json:"conclusion"`
	ExitCode   *int            `json:"exit_code"`
	LogBytes   int64           `json:"log_bytes"`
	StartedAt  *time.Time      `json:"started_at"`
	FinishedAt *time.Time      `json:"completed_at"`
	Diagnosis  json.RawMessage `json:"diagnosis"`
}

type job struct {
	ID             string          `json:"id"`
	JobKey         string          `json:"job_key"`
	Key            string          `json:"key"`
	Name           string          `json:"name"`
	Status         string          `json:"status"`
	Conclusion     string          `json:"conclusion"`
	RunnerID       string          `json:"runner_id"`
	RunnerName     string          `json:"runner_name"`
	Attempt        int             `json:"attempt"`
	Error          string          `json:"error"`
	Classification string          `json:"classification,omitempty"`
	QueueReason    string          `json:"queue_reason"`
	RunsOn         []string        `json:"runs_on"`
	QueuedAt       *time.Time      `json:"queued_at"`
	StartedAt      *time.Time      `json:"started_at"`
	CompletedAt    *time.Time      `json:"completed_at"`
	Metrics        json.RawMessage `json:"metrics"`
	Steps          []step          `json:"steps"`
}

type runDetail struct {
	Run      run             `json:"run"`
	Jobs     []job           `json:"jobs"`
	Plan     json.RawMessage `json:"plan"`
	Workflow struct {
		ID        string `json:"id"`
		Path      string `json:"path"`
		Name      string `json:"name"`
		CommitSHA string `json:"commit_sha"`
	} `json:"workflow"`
}

type runnerRow struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Hostname        string     `json:"hostname"`
	OS              string     `json:"os"`
	Arch            string     `json:"arch"`
	CPUs            int        `json:"cpus"`
	MemoryBytes     int64      `json:"memory_bytes"`
	DiskBytes       int64      `json:"disk_bytes"`
	Labels          []string   `json:"labels"`
	Version         string     `json:"version"`
	Status          string     `json:"status"`
	Draining        bool       `json:"draining"`
	WakeOnDemand    bool       `json:"wake_on_demand"`
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at"`
	CPUPercent      *float64   `json:"cpu_percent"`
	MemUsedBytes    *int64     `json:"mem_used_bytes"`
	DiskFreeBytes   *int64     `json:"disk_free_bytes"`
	MetricsAt       *time.Time `json:"metrics_at"`
	ActiveJobs      int        `json:"active_jobs"`
}

type artifact struct {
	ID          string     `json:"id"`
	RunID       string     `json:"run_id"`
	Name        string     `json:"name"`
	SizeBytes   int64      `json:"size_bytes"`
	SHA256      string     `json:"sha256"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   *time.Time `json:"expires_at"`
	RunNumber   int        `json:"run_number"`
	ProjectSlug string     `json:"project_slug"`
}

type cacheEntry struct {
	ID         string     `json:"id"`
	ProjectID  string     `json:"project_id"`
	Key        string     `json:"key"`
	Scope      string     `json:"scope"`
	SizeBytes  int64      `json:"size_bytes"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt time.Time  `json:"last_used_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
}

type secretSummary struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	ProjectID *string    `json:"project_id"`
	UpdatedAt *time.Time `json:"updated_at"`
}

type stepLog struct {
	Text       string `json:"text"`
	NextOffset int64  `json:"next_offset"`
	Size       int64  `json:"size"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}
