package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Environment is a deployment target; protected environments gate jobs on
// explicit approvals before a runner may claim them.
type Environment struct {
	ID                uuid.UUID `json:"id"`
	ProjectID         uuid.UUID `json:"project_id"`
	Name              string    `json:"name"`
	Protected         bool      `json:"protected"`
	RequiredApprovals int       `json:"required_approvals"`
	CreatedAt         time.Time `json:"created_at"`
}

func scanEnvironment(row pgx.Row) (Environment, error) {
	var e Environment
	err := row.Scan(&e.ID, &e.ProjectID, &e.Name, &e.Protected, &e.RequiredApprovals, &e.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Environment{}, ErrNotFound
	}
	return e, err
}

const envSelect = `SELECT id, project_id, name, protected, required_approvals, created_at FROM environments`

// EnsureEnvironment returns the named environment, creating it unprotected on
// first use — the same convention GitHub applies to environment names in YAML.
func (s *Store) EnsureEnvironment(ctx context.Context, projectID uuid.UUID, name string) (Environment, error) {
	return scanEnvironment(s.pool.QueryRow(ctx, `
		INSERT INTO environments (project_id, name)
		VALUES ($1, $2)
		ON CONFLICT (project_id, name) DO UPDATE SET name = EXCLUDED.name
		RETURNING id, project_id, name, protected, required_approvals, created_at`,
		projectID, name))
}

// Environment returns the named environment or ErrNotFound.
func (s *Store) Environment(ctx context.Context, projectID uuid.UUID, name string) (Environment, error) {
	return scanEnvironment(s.pool.QueryRow(ctx, envSelect+`
		WHERE project_id = $1 AND name = $2`, projectID, name))
}

func (s *Store) ListEnvironments(ctx context.Context, projectID uuid.UUID) ([]Environment, error) {
	rows, err := s.pool.Query(ctx, envSelect+` WHERE project_id = $1 ORDER BY name`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Environment{}
	for rows.Next() {
		e, err := scanEnvironment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PutEnvironment upserts protection settings for a named environment.
func (s *Store) PutEnvironment(ctx context.Context, projectID uuid.UUID, name string, protected bool, requiredApprovals int) (Environment, error) {
	return scanEnvironment(s.pool.QueryRow(ctx, `
		INSERT INTO environments (project_id, name, protected, required_approvals)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (project_id, name)
		DO UPDATE SET protected = EXCLUDED.protected, required_approvals = EXCLUDED.required_approvals
		RETURNING id, project_id, name, protected, required_approvals, created_at`,
		projectID, name, protected, requiredApprovals))
}

func (s *Store) DeleteEnvironment(ctx context.Context, projectID uuid.UUID, name string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM environments WHERE project_id = $1 AND name = $2`, projectID, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Approval records one user's sign-off on a deployment.
type Approval struct {
	UserID uuid.UUID `json:"user_id"`
	Email  string    `json:"email"`
	At     time.Time `json:"at"`
}

// Deployment records a job's use of an environment.
type Deployment struct {
	ID              uuid.UUID       `json:"id"`
	ProjectID       uuid.UUID       `json:"project_id"`
	EnvironmentID   uuid.UUID       `json:"environment_id"`
	EnvironmentName string          `json:"environment_name"`
	RunID           uuid.UUID       `json:"run_id"`
	JobID           uuid.UUID       `json:"job_id"`
	Ref             string          `json:"ref"`
	SHA             string          `json:"sha"`
	Status          string          `json:"status"`
	URL             string          `json:"url"`
	Approvals       json.RawMessage `json:"approvals"`
	Required        int             `json:"required_approvals"`
	RunNumber       int             `json:"run_number"`
	ProjectSlug     string          `json:"project_slug"`
	JobName         string          `json:"job_name"`
	CreatedAt       time.Time       `json:"created_at"`
	CompletedAt     *time.Time      `json:"completed_at"`
}

const deploymentSelect = `
	SELECT d.id, d.project_id, d.environment_id, e.name, d.run_id, d.job_id,
	       d.ref, d.sha, d.status, d.url, d.approvals, e.required_approvals,
	       r.run_number, p.slug, j.name, d.created_at, d.completed_at
	FROM deployments d
	JOIN environments e ON e.id = d.environment_id
	JOIN workflow_runs r ON r.id = d.run_id
	JOIN projects p ON p.id = d.project_id
	JOIN jobs j ON j.id = d.job_id`

func scanDeployment(row pgx.Row) (Deployment, error) {
	var d Deployment
	err := row.Scan(&d.ID, &d.ProjectID, &d.EnvironmentID, &d.EnvironmentName, &d.RunID, &d.JobID,
		&d.Ref, &d.SHA, &d.Status, &d.URL, &d.Approvals, &d.Required,
		&d.RunNumber, &d.ProjectSlug, &d.JobName, &d.CreatedAt, &d.CompletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Deployment{}, ErrNotFound
	}
	return d, err
}

// CreateDeployment records a queued deployment for a job that named an
// environment. Called once when the job unblocks, before it parks or queues.
func (s *Store) CreateDeployment(ctx context.Context, projectID, envID, runID, jobID uuid.UUID, ref, sha, url string) (Deployment, error) {
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, `
		INSERT INTO deployments (project_id, environment_id, run_id, job_id, ref, sha, status, url)
		VALUES ($1, $2, $3, $4, $5, $6, 'queued', $7)
		RETURNING id`, projectID, envID, runID, jobID, ref, sha, url).Scan(&id)
	if err != nil {
		return Deployment{}, err
	}
	return s.deployment(ctx, id)
}

func (s *Store) deployment(ctx context.Context, id uuid.UUID) (Deployment, error) {
	return scanDeployment(s.pool.QueryRow(ctx, deploymentSelect+` WHERE d.id = $1`, id))
}

// DeploymentForJob returns the newest deployment of a job (a job deploys at
// most once per attempt; retries create a fresh row is deliberately avoided —
// one row per job keeps approvals anchored).
func (s *Store) DeploymentForJob(ctx context.Context, jobID uuid.UUID) (Deployment, error) {
	return scanDeployment(s.pool.QueryRow(ctx, deploymentSelect+`
		WHERE d.job_id = $1 ORDER BY d.created_at DESC LIMIT 1`, jobID))
}

func (s *Store) DeploymentsForRun(ctx context.Context, runID uuid.UUID) ([]Deployment, error) {
	return s.scanDeployments(s.pool.Query(ctx, deploymentSelect+`
		WHERE d.run_id = $1 ORDER BY d.created_at`, runID))
}

func (s *Store) ListDeployments(ctx context.Context, orgID uuid.UUID, projectID *uuid.UUID, limit int) ([]Deployment, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return s.scanDeployments(s.pool.Query(ctx, deploymentSelect+`
		WHERE p.org_id = $1 AND ($2::uuid IS NULL OR d.project_id = $2)
		ORDER BY d.created_at DESC LIMIT $3`, orgID, projectID, limit))
}

func (s *Store) scanDeployments(rows pgx.Rows, err error) ([]Deployment, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Deployment{}
	for rows.Next() {
		d, err := scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ApproveDeployment records the user's approval on the job's queued
// deployment. When the approval count reaches the environment's requirement
// the job is released to the queue. Returns the updated deployment and
// whether the job was released.
func (s *Store) ApproveDeployment(ctx context.Context, jobID, userID uuid.UUID, email string) (Deployment, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Deployment{}, false, err
	}
	defer tx.Rollback(ctx)

	var depID uuid.UUID
	var approvals json.RawMessage
	var required int
	err = tx.QueryRow(ctx, `
		SELECT d.id, d.approvals, e.required_approvals
		FROM deployments d
		JOIN environments e ON e.id = d.environment_id
		JOIN jobs j ON j.id = d.job_id
		WHERE d.job_id = $1 AND d.status = 'queued' AND j.status = 'waiting_approval'
		ORDER BY d.created_at DESC LIMIT 1
		FOR UPDATE OF d`, jobID).Scan(&depID, &approvals, &required)
	if errors.Is(err, pgx.ErrNoRows) {
		return Deployment{}, false, fmt.Errorf("no pending deployment approval for this job")
	}
	if err != nil {
		return Deployment{}, false, err
	}

	var list []Approval
	_ = json.Unmarshal(approvals, &list)
	for _, a := range list {
		if a.UserID == userID {
			return Deployment{}, false, fmt.Errorf("already approved by this user")
		}
	}
	list = append(list, Approval{UserID: userID, Email: email, At: time.Now()})
	raw, err := json.Marshal(list)
	if err != nil {
		return Deployment{}, false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE deployments SET approvals = $2 WHERE id = $1`, depID, raw); err != nil {
		return Deployment{}, false, err
	}

	released := len(list) >= required
	if released {
		if _, err := tx.Exec(ctx, `
			UPDATE jobs SET status = 'queued', queued_at = now()
			WHERE id = $1 AND status = 'waiting_approval'`, jobID); err != nil {
			return Deployment{}, false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Deployment{}, false, err
	}
	dep, err := s.DeploymentForJob(ctx, jobID)
	return dep, released, err
}

// RejectDeployment refuses the job's pending deployment: the deployment is
// marked failed and the job cancelled.
func (s *Store) RejectDeployment(ctx context.Context, jobID uuid.UUID) (Deployment, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Deployment{}, err
	}
	defer tx.Rollback(ctx)

	var depID uuid.UUID
	err = tx.QueryRow(ctx, `
		SELECT d.id FROM deployments d
		JOIN jobs j ON j.id = d.job_id
		WHERE d.job_id = $1 AND d.status = 'queued' AND j.status = 'waiting_approval'
		ORDER BY d.created_at DESC LIMIT 1`, jobID).Scan(&depID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Deployment{}, fmt.Errorf("no pending deployment approval for this job")
	}
	if err != nil {
		return Deployment{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE deployments SET status = 'failure', completed_at = now() WHERE id = $1`, depID); err != nil {
		return Deployment{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE jobs SET status = 'cancelled', conclusion = 'cancelled', completed_at = now(),
			error = 'deployment rejected'
		WHERE id = $1 AND status = 'waiting_approval'`, jobID); err != nil {
		return Deployment{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE steps SET status = 'cancelled', conclusion = 'cancelled', completed_at = now()
		WHERE job_id = $1 AND status = 'queued'`, jobID); err != nil {
		return Deployment{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Deployment{}, err
	}
	return s.DeploymentForJob(ctx, jobID)
}

// SetJobWaitingApproval parks a job behind its environment's approval gate.
func (s *Store) SetJobWaitingApproval(ctx context.Context, jobID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE jobs SET status = 'waiting_approval' WHERE id = $1 AND status = 'waiting'`, jobID)
	return err
}

// failDeploymentsForJobs closes open deployments of cancelled or failed jobs.
func (s *Store) failDeploymentsForJobs(ctx context.Context, jobIDs []uuid.UUID) {
	if len(jobIDs) == 0 {
		return
	}
	_, _ = s.pool.Exec(ctx, `
		UPDATE deployments SET status = 'failure', completed_at = now()
		WHERE job_id = ANY($1) AND status IN ('queued', 'in_progress')`, jobIDs)
}
