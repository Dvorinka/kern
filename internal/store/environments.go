package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrNotReviewer is returned when the caller is outside the environment's
// reviewer list. An empty list means any org member. Owners and admins bypass
// the list so a rule cannot deadlock the org.
var ErrNotReviewer = errors.New("not a reviewer for this environment")

// Environment is a deployment target. When protected, its rules can require
// named reviewers, a wait, and a branch pattern before a job may run.
type Environment struct {
	ID                uuid.UUID `json:"id"`
	ProjectID         uuid.UUID `json:"project_id"`
	Name              string    `json:"name"`
	Protected         bool      `json:"protected"`
	RequiredApprovals int       `json:"required_approvals"`
	Reviewers         []string  `json:"reviewers"`
	ReviewerEmails    []string  `json:"reviewer_emails"`
	WaitMinutes       int       `json:"wait_minutes"`
	BranchPolicy      string    `json:"branch_policy"`
	BranchPatterns    []string  `json:"branch_patterns"`
	CreatedAt         time.Time `json:"created_at"`
}

// EnvironmentInput is the full protection rule written by PutEnvironment.
// Omitted slices are stored empty; BranchPolicy defaults to "all".
type EnvironmentInput struct {
	Protected         bool
	RequiredApprovals int
	Reviewers         []string
	WaitMinutes       int
	BranchPolicy      string
	BranchPatterns    []string
}

func (in EnvironmentInput) normalized() EnvironmentInput {
	if in.Reviewers == nil {
		in.Reviewers = []string{}
	}
	if in.BranchPatterns == nil {
		in.BranchPatterns = []string{}
	}
	if in.BranchPolicy == "" {
		in.BranchPolicy = "all"
	}
	return in
}

func scanEnvironment(row pgx.Row) (Environment, error) {
	var e Environment
	err := row.Scan(
		&e.ID, &e.ProjectID, &e.Name, &e.Protected, &e.RequiredApprovals,
		&e.Reviewers, &e.ReviewerEmails, &e.WaitMinutes, &e.BranchPolicy, &e.BranchPatterns,
		&e.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Environment{}, ErrNotFound
	}
	if err != nil {
		return Environment{}, err
	}
	if e.Reviewers == nil {
		e.Reviewers = []string{}
	}
	if e.ReviewerEmails == nil {
		e.ReviewerEmails = []string{}
	}
	if e.BranchPatterns == nil {
		e.BranchPatterns = []string{}
	}
	return e, nil
}

const envCols = `id, project_id, name, protected, required_approvals, reviewers,
	COALESCE((SELECT array_agg(u.email ORDER BY u.email) FROM users u WHERE u.id::text = ANY(environments.reviewers)), '{}'),
	wait_minutes, branch_policy, branch_patterns, created_at`

const envSelect = `SELECT ` + envCols + ` FROM environments`

// EnsureEnvironment returns the named environment, creating it unprotected on
// first use. The conflict update does not touch protection rules.
func (s *Store) EnsureEnvironment(ctx context.Context, projectID uuid.UUID, name string) (Environment, error) {
	return scanEnvironment(s.pool.QueryRow(ctx, `
		INSERT INTO environments (project_id, name)
		VALUES ($1, $2)
		ON CONFLICT (project_id, name) DO UPDATE SET name = EXCLUDED.name
		RETURNING `+envCols, projectID, name))
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

// PutEnvironment upserts the full protection rule for a named environment.
func (s *Store) PutEnvironment(ctx context.Context, projectID uuid.UUID, name string, in EnvironmentInput) (Environment, error) {
	in = in.normalized()
	return scanEnvironment(s.pool.QueryRow(ctx, `
		INSERT INTO environments (
			project_id, name, protected, required_approvals,
			reviewers, wait_minutes, branch_policy, branch_patterns)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (project_id, name) DO UPDATE SET
			protected = EXCLUDED.protected,
			required_approvals = EXCLUDED.required_approvals,
			reviewers = EXCLUDED.reviewers,
			wait_minutes = EXCLUDED.wait_minutes,
			branch_policy = EXCLUDED.branch_policy,
			branch_patterns = EXCLUDED.branch_patterns
		RETURNING `+envCols,
		projectID, name, in.Protected, in.RequiredApprovals,
		in.Reviewers, in.WaitMinutes, in.BranchPolicy, in.BranchPatterns))
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

// RefBranch strips the Git ref prefix so a pattern can name the branch.
func RefBranch(ref string) string {
	ref = strings.TrimPrefix(ref, "refs/heads/")
	return strings.TrimPrefix(ref, "refs/tags/")
}

// BranchAllowed reports whether ref may deploy under the policy.
// policy "all" allows everything. "selected" matches path patterns against
// the short branch name and the full ref. * does not cross /.
func BranchAllowed(policy string, patterns []string, ref string) bool {
	if policy != "selected" {
		return true
	}
	name := RefBranch(ref)
	for _, p := range patterns {
		if ok, err := path.Match(p, name); err == nil && ok {
			return true
		}
		if ok, err := path.Match(p, ref); err == nil && ok {
			return true
		}
	}
	return false
}

// Holds reports whether a protected environment parks the job before a runner
// may claim it. A branch pattern alone does not park: a denied ref fails the job.
func (e Environment) Holds() bool {
	return e.Protected && (e.RequiredApprovals > 0 || e.WaitMinutes > 0)
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
	ReviewerEmails  []string        `json:"reviewer_emails"`
	WaitUntil       *time.Time      `json:"wait_until"`
	RunNumber       int             `json:"run_number"`
	ProjectSlug     string          `json:"project_slug"`
	JobName         string          `json:"job_name"`
	CreatedAt       time.Time       `json:"created_at"`
	CompletedAt     *time.Time      `json:"completed_at"`
}

const deploymentSelect = `
	SELECT d.id, d.project_id, d.environment_id, e.name, d.run_id, d.job_id,
	       d.ref, d.sha, d.status, d.url, d.approvals, e.required_approvals,
	       COALESCE((SELECT array_agg(u.email ORDER BY u.email) FROM users u WHERE u.id::text = ANY(e.reviewers)), '{}'),
	       d.wait_until,
	       r.run_number, p.slug, j.name, d.created_at, d.completed_at
	FROM deployments d
	JOIN environments e ON e.id = d.environment_id
	JOIN workflow_runs r ON r.id = d.run_id
	JOIN projects p ON p.id = d.project_id
	JOIN jobs j ON j.id = d.job_id`

func scanDeployment(row pgx.Row) (Deployment, error) {
	var d Deployment
	err := row.Scan(&d.ID, &d.ProjectID, &d.EnvironmentID, &d.EnvironmentName, &d.RunID, &d.JobID,
		&d.Ref, &d.SHA, &d.Status, &d.URL, &d.Approvals, &d.Required, &d.ReviewerEmails, &d.WaitUntil,
		&d.RunNumber, &d.ProjectSlug, &d.JobName, &d.CreatedAt, &d.CompletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Deployment{}, ErrNotFound
	}
	if err != nil {
		return Deployment{}, err
	}
	if d.ReviewerEmails == nil {
		d.ReviewerEmails = []string{}
	}
	return d, nil
}

// CreateDeployment records a queued deployment for a job that named an
// environment. waitUntil is the absolute time the wait rule expires; nil
// means there is no wait. Called once, before the job parks or queues.
func (s *Store) CreateDeployment(ctx context.Context, projectID, envID, runID, jobID uuid.UUID, ref, sha, url string, waitUntil *time.Time) (Deployment, error) {
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, `
		INSERT INTO deployments (project_id, environment_id, run_id, job_id, ref, sha, status, url, wait_until)
		VALUES ($1, $2, $3, $4, $5, $6, 'queued', $7, $8)
		RETURNING id`, projectID, envID, runID, jobID, ref, sha, url, waitUntil).Scan(&id)
	if err != nil {
		return Deployment{}, err
	}
	return s.deployment(ctx, id)
}

func (s *Store) deployment(ctx context.Context, id uuid.UUID) (Deployment, error) {
	return scanDeployment(s.pool.QueryRow(ctx, deploymentSelect+` WHERE d.id = $1`, id))
}

// DeploymentForJob returns the newest deployment of a job. One row per job
// keeps approvals anchored across the wait.
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

func reviewerAllowed(reviewers []string, userID uuid.UUID, role string) bool {
	if role == "owner" || role == "admin" {
		return true
	}
	if len(reviewers) == 0 {
		return true
	}
	id := userID.String()
	for _, r := range reviewers {
		if r == id {
			return true
		}
	}
	return false
}

// ApproveDeployment records the user's approval. The job is released only
// when the approval count is met and the wait, if any, has elapsed.
func (s *Store) ApproveDeployment(ctx context.Context, jobID, userID uuid.UUID, email, role string) (Deployment, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Deployment{}, false, err
	}
	defer tx.Rollback(ctx)

	var depID uuid.UUID
	var approvals json.RawMessage
	var required int
	var reviewers []string
	var waitUntil *time.Time
	err = tx.QueryRow(ctx, `
		SELECT d.id, d.approvals, e.required_approvals, e.reviewers, d.wait_until
		FROM deployments d
		JOIN environments e ON e.id = d.environment_id
		JOIN jobs j ON j.id = d.job_id
		WHERE d.job_id = $1 AND d.status = 'queued' AND j.status = 'waiting_approval'
		ORDER BY d.created_at DESC LIMIT 1
		FOR UPDATE OF d`, jobID).Scan(&depID, &approvals, &required, &reviewers, &waitUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return Deployment{}, false, fmt.Errorf("no pending deployment approval for this job")
	}
	if err != nil {
		return Deployment{}, false, err
	}
	if !reviewerAllowed(reviewers, userID, role) {
		return Deployment{}, false, ErrNotReviewer
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

	released := len(list) >= required && (waitUntil == nil || !waitUntil.After(time.Now()))
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

// RejectDeployment refuses the job's pending deployment. The same reviewer
// rule as approval applies.
func (s *Store) RejectDeployment(ctx context.Context, jobID, userID uuid.UUID, role string) (Deployment, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Deployment{}, err
	}
	defer tx.Rollback(ctx)

	var depID uuid.UUID
	var reviewers []string
	err = tx.QueryRow(ctx, `
		SELECT d.id, e.reviewers FROM deployments d
		JOIN environments e ON e.id = d.environment_id
		JOIN jobs j ON j.id = d.job_id
		WHERE d.job_id = $1 AND d.status = 'queued' AND j.status = 'waiting_approval'
		ORDER BY d.created_at DESC LIMIT 1`, jobID).Scan(&depID, &reviewers)
	if errors.Is(err, pgx.ErrNoRows) {
		return Deployment{}, fmt.Errorf("no pending deployment approval for this job")
	}
	if err != nil {
		return Deployment{}, err
	}
	if !reviewerAllowed(reviewers, userID, role) {
		return Deployment{}, ErrNotReviewer
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

// FailDeployment closes a deployment that was refused before the job parked,
// typically because the ref is outside the branch policy. The job fails.
func (s *Store) FailDeployment(ctx context.Context, jobID uuid.UUID, reason string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		UPDATE deployments SET status = 'failure', completed_at = now()
		WHERE job_id = $1 AND status = 'queued'`, jobID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE jobs SET status = 'completed', conclusion = 'failure', completed_at = now(), error = $2
		WHERE id = $1 AND status = 'waiting'`, jobID, reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("job %s is not waiting", jobID)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE steps SET status = 'cancelled', conclusion = 'cancelled', completed_at = now()
		WHERE job_id = $1 AND status = 'queued'`, jobID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ReleaseDueDeployments queues parked jobs whose approvals are met and whose
// wait has elapsed. Opening the environment also releases them.
func (s *Store) ReleaseDueDeployments(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE jobs j
		SET status = 'queued', queued_at = now()
		FROM deployments d
		JOIN environments e ON e.id = d.environment_id
		WHERE d.job_id = j.id
		  AND j.status = 'waiting_approval'
		  AND d.status = 'queued'
		  AND (
		    NOT e.protected
		    OR (
		      (d.wait_until IS NULL OR d.wait_until <= now())
		      AND COALESCE(jsonb_array_length(d.approvals), 0) >= e.required_approvals
		    )
		  )`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// SetJobWaitingApproval parks a job behind its environment's approval or wait.
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
