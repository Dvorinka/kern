// Package store is the PostgreSQL persistence layer. It uses pgx directly with
// explicit scans so every column that crosses the boundary is accounted for.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/dvorinka/kern/migrations"
)

type Store struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConns = 16
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

// Pool exposes the underlying pool for health checks.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Migrate applies pending migrations. Runs are idempotent.
func (s *Store) Migrate(ctx context.Context, dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open migration connection: %w", err)
	}
	defer db.Close()
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set migration dialect: %w", err)
	}
	if err := goose.UpContext(ctx, db, "."); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}

type Org struct {
	ID        uuid.UUID `json:"id"`
	Slug      string    `json:"slug"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type User struct {
	ID           uuid.UUID `json:"id"`
	Email        string    `json:"email"`
	Name         string    `json:"name"`
	PasswordHash string    `json:"-"`
	OrgID        uuid.UUID `json:"org_id"`
	Role         string    `json:"role"`
	CreatedAt    time.Time `json:"created_at"`
}

type Project struct {
	ID            uuid.UUID `json:"id"`
	OrgID         uuid.UUID `json:"org_id"`
	Slug          string    `json:"slug"`
	Name          string    `json:"name"`
	Provider      string    `json:"provider"`
	RepoFullName  string    `json:"repo_full_name"`
	RepoURL       string    `json:"repo_url"`
	DefaultBranch string    `json:"default_branch"`
	LocalPath     string    `json:"local_path"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type Workflow struct {
	ID        uuid.UUID `json:"id"`
	ProjectID uuid.UUID `json:"project_id"`
	Path      string    `json:"path"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type WorkflowVersion struct {
	ID          uuid.UUID       `json:"id"`
	WorkflowID  uuid.UUID       `json:"workflow_id"`
	CommitSHA   string          `json:"commit_sha"`
	Content     string          `json:"content"`
	Parsed      json.RawMessage `json:"parsed,omitempty"`
	Valid       bool            `json:"valid"`
	Diagnostics json.RawMessage `json:"diagnostics"`
	CreatedAt   time.Time       `json:"created_at"`
}

type Run struct {
	ID                uuid.UUID       `json:"id"`
	ProjectID         uuid.UUID       `json:"project_id"`
	WorkflowID        uuid.UUID       `json:"workflow_id"`
	WorkflowVersionID uuid.UUID       `json:"workflow_version_id"`
	RunNumber         int             `json:"run_number"`
	Event             string          `json:"event"`
	Ref               string          `json:"ref"`
	SHA               string          `json:"sha"`
	CommitMessage     string          `json:"commit_message"`
	Actor             string          `json:"actor"`
	Status            string          `json:"status"`
	Conclusion        string          `json:"conclusion"`
	TrustLevel        string          `json:"trust_level"`
	Source            json.RawMessage `json:"source"`
	ConcurrencyGroup  string          `json:"concurrency_group"`
	StartedAt         *time.Time      `json:"started_at"`
	CompletedAt       *time.Time      `json:"completed_at"`
	CreatedAt         time.Time       `json:"created_at"`
	// Joined fields.
	WorkflowName string `json:"workflow_name"`
	WorkflowPath string `json:"workflow_path"`
	ProjectSlug  string `json:"project_slug"`
	ProjectName  string `json:"project_name"`
}

type Job struct {
	ID              uuid.UUID       `json:"id"`
	RunID           uuid.UUID       `json:"run_id"`
	JobKey          string          `json:"job_key"`
	Key             string          `json:"key"`
	Name            string          `json:"name"`
	Status          string          `json:"status"`
	Conclusion      string          `json:"conclusion"`
	Needs           []string        `json:"needs"`
	RunsOn          []string        `json:"runs_on"`
	Image           string          `json:"image"`
	Matrix          json.RawMessage `json:"matrix"`
	Env             json.RawMessage `json:"env"`
	IfCondition     string          `json:"if_condition"`
	TimeoutSeconds  int             `json:"timeout_seconds"`
	ContinueOnError bool            `json:"continue_on_error"`
	FailFast        bool            `json:"fail_fast"`
	Priority        int             `json:"priority"`
	Attempt         int             `json:"attempt"`
	RunnerID        *uuid.UUID      `json:"runner_id"`
	RunnerName      string          `json:"runner_name"`
	QueuedAt        time.Time       `json:"queued_at"`
	StartedAt       *time.Time      `json:"started_at"`
	CompletedAt     *time.Time      `json:"completed_at"`
	LeaseExpiresAt  *time.Time      `json:"lease_expires_at"`
	Error           string          `json:"error"`
	Outputs         json.RawMessage `json:"outputs"`
	OutputsSpec     json.RawMessage `json:"outputs_spec"`
	Steps           []Step          `json:"steps,omitempty"`
}

// EnvMap decodes the job environment.
func (j Job) EnvMap() (map[string]string, error) {
	out := map[string]string{}
	if len(j.Env) == 0 || string(j.Env) == "{}" {
		return out, nil
	}
	if err := json.Unmarshal(j.Env, &out); err != nil {
		return nil, err
	}
	return out, nil
}

type Step struct {
	ID          uuid.UUID       `json:"id"`
	JobID       uuid.UUID       `json:"job_id"`
	Number      int             `json:"number"`
	Key         string          `json:"key"`
	Name        string          `json:"name"`
	Status      string          `json:"status"`
	Conclusion  string          `json:"conclusion"`
	ExitCode    *int            `json:"exit_code"`
	StartedAt   *time.Time      `json:"started_at"`
	CompletedAt *time.Time      `json:"completed_at"`
	Spec        json.RawMessage `json:"spec,omitempty"`
	LogRef      string          `json:"-"`
	LogBytes    int64           `json:"log_bytes"`
	Diagnosis   json.RawMessage `json:"diagnosis,omitempty"`
	SummaryRef  string          `json:"-"`
	HasSummary  bool            `json:"has_summary"`
}

type Runner struct {
	ID              uuid.UUID  `json:"id"`
	OrgID           uuid.UUID  `json:"org_id"`
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
	Ephemeral       bool       `json:"ephemeral"`
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at"`
	CPUPercent      *float64   `json:"cpu_percent"`
	MemUsedBytes    *int64     `json:"mem_used_bytes"`
	DiskFreeBytes   *int64     `json:"disk_free_bytes"`
	MetricsAt       *time.Time `json:"metrics_at"`
	CreatedAt       time.Time  `json:"created_at"`
}

// HostMetrics is the live utilization snapshot a runner sends with each
// heartbeat. Pointer fields let the server distinguish "not reported" from 0.
type HostMetrics struct {
	CPUPercent    *float64 `json:"cpu_percent"`
	MemUsedBytes  *int64   `json:"mem_used_bytes"`
	DiskFreeBytes *int64   `json:"disk_free_bytes"`
}

type Secret struct {
	ID         uuid.UUID  `json:"id"`
	OrgID      uuid.UUID  `json:"org_id"`
	ProjectID  *uuid.UUID `json:"project_id"`
	Name       string     `json:"name"`
	Ciphertext []byte     `json:"-"`
	Nonce      []byte     `json:"-"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

type Overview struct {
	RunsTotal       int64      `json:"runs_total"`
	RunsSuccess     int64      `json:"runs_success"`
	RunsFailure     int64      `json:"runs_failure"`
	RunsActive      int64      `json:"runs_active"`
	JobsQueued      int64      `json:"jobs_queued"`
	JobsRunning     int64      `json:"jobs_running"`
	RunnersOnline   int64      `json:"runners_online"`
	RunnersTotal    int64      `json:"runners_total"`
	AvgQueueSeconds float64    `json:"avg_queue_seconds"`
	AvgRunSeconds   float64    `json:"avg_run_seconds"`
	P95QueueSeconds float64    `json:"p95_queue_seconds"`
	LastRunAt       *time.Time `json:"last_run_at"`
}

// page applies limit/offset bounds shared by list queries.
func page(limit, offset int) (int, int) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func nullableUUID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}
