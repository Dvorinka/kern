package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// JobSeed is a compiled job ready to be persisted.
type JobSeed struct {
	Key             string
	JobKey          string
	Name            string
	Needs           []string
	RunsOn          []string
	Image           string
	Matrix          map[string]any
	Env             map[string]string
	IfCondition     string
	TimeoutSeconds  int
	ContinueOnError bool
	FailFast        bool
	Priority        int
	OutputsSpec     map[string]string
	Inputs          map[string]string
	Services        []ServiceSeed
	Environment     string
	EnvironmentURL  string
	// SplitN and SplitAuto are consumed before insert. They are not columns.
	SplitN    int
	SplitAuto bool
	Steps     []StepSeed
}

// ServiceSeed is a service container spec persisted with the job.
type ServiceSeed struct {
	Name    string            `json:"name"`
	Image   string            `json:"image"`
	Env     map[string]string `json:"env,omitempty"`
	Options []string          `json:"options,omitempty"`
}

type StepSeed struct {
	Key  string
	Name string
	Spec any
}

type CreateRunParams struct {
	ProjectID         uuid.UUID
	WorkflowID        uuid.UUID
	WorkflowVersionID uuid.UUID
	Event             string
	Ref               string
	SHA               string
	CommitMessage     string
	Actor             string
	TrustLevel        string
	Source            map[string]any
	ConcurrencyGroup  string
	CancelInProgress  bool
	// HoldForApproval parks the run in awaiting_approval: jobs stay 'waiting'
	// and nothing is promoted until ApproveRunHold releases it.
	HoldForApproval bool
}

// CreateRun persists a run and its compiled jobs atomically. Run numbers are allocated
// per workflow; a unique violation (concurrent insert) is retried.
func (s *Store) CreateRun(ctx context.Context, p CreateRunParams, jobs []JobSeed) (Run, error) {
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		run, err := s.createRunOnce(ctx, p, jobs)
		if err == nil {
			return run, nil
		}
		lastErr = err
	}
	return Run{}, fmt.Errorf("create run: %w", lastErr)
}

func (s *Store) createRunOnce(ctx context.Context, p CreateRunParams, jobs []JobSeed) (Run, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback(ctx)

	source, err := json.Marshal(p.Source)
	if err != nil {
		return Run{}, err
	}
	if p.TrustLevel == "" {
		p.TrustLevel = "trusted"
	}

	var id uuid.UUID
	status := "queued"
	if p.HoldForApproval {
		status = "awaiting_approval"
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO workflow_runs
			(project_id, workflow_id, workflow_version_id, run_number, event, ref, sha,
			 commit_message, actor, trust_level, source, concurrency_group, cancel_in_progress, status)
		VALUES ($1, $2, $3,
		        (SELECT COALESCE(MAX(run_number), 0) + 1 FROM workflow_runs WHERE workflow_id = $2),
		        $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING id`,
		p.ProjectID, p.WorkflowID, p.WorkflowVersionID, p.Event, p.Ref, p.SHA,
		p.CommitMessage, p.Actor, p.TrustLevel, source, p.ConcurrencyGroup, p.CancelInProgress,
		status).Scan(&id)
	if err != nil {
		return Run{}, err
	}

	for _, j := range jobs {
		matrix, err := json.Marshal(j.Matrix)
		if err != nil {
			return Run{}, err
		}
		env, err := json.Marshal(j.Env)
		if err != nil {
			return Run{}, err
		}
		outputsSpec, err := json.Marshal(j.OutputsSpec)
		if err != nil {
			return Run{}, err
		}
		inputs, err := json.Marshal(j.Inputs)
		if err != nil {
			return Run{}, err
		}
		services, err := json.Marshal(j.Services)
		if err != nil {
			return Run{}, err
		}
		if j.TimeoutSeconds <= 0 {
			j.TimeoutSeconds = 3600
		}
		var jobID uuid.UUID
		err = tx.QueryRow(ctx, `
			INSERT INTO jobs
				(run_id, job_key, key, name, status, needs, runs_on, image, matrix, env,
				 if_condition, timeout_seconds, continue_on_error, fail_fast, priority, outputs_spec,
				 environment, environment_url, inputs, services)
			VALUES ($1, $2, $3, $4, 'waiting', $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)
			RETURNING id`,
			id, j.JobKey, j.Key, j.Name, j.Needs, j.RunsOn, j.Image, matrix, env,
			j.IfCondition, j.TimeoutSeconds, j.ContinueOnError, j.FailFast, j.Priority, outputsSpec,
			j.Environment, j.EnvironmentURL, inputs, services).Scan(&jobID)
		if err != nil {
			return Run{}, fmt.Errorf("insert job %s: %w", j.Key, err)
		}
		for i, st := range j.Steps {
			spec, err := json.Marshal(st.Spec)
			if err != nil {
				return Run{}, err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO steps (job_id, number, key, name, spec) VALUES ($1, $2, $3, $4, $5)`,
				jobID, i+1, st.Key, st.Name, spec); err != nil {
				return Run{}, fmt.Errorf("insert step %s: %w", st.Key, err)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Run{}, err
	}
	return s.Run(ctx, id)
}

const runSelect = `
	SELECT r.id, r.project_id, r.workflow_id, r.workflow_version_id, r.run_number, r.event,
	       r.ref, r.sha, r.commit_message, r.actor, r.status, r.conclusion, r.trust_level,
	       r.source, r.concurrency_group, r.started_at, r.completed_at, r.created_at,
	       w.name, w.path, p.slug, p.name
	FROM workflow_runs r
	JOIN workflows w ON w.id = r.workflow_id
	JOIN projects p ON p.id = r.project_id`

func scanRun(row pgx.Row) (Run, error) {
	var r Run
	err := row.Scan(&r.ID, &r.ProjectID, &r.WorkflowID, &r.WorkflowVersionID, &r.RunNumber,
		&r.Event, &r.Ref, &r.SHA, &r.CommitMessage, &r.Actor, &r.Status, &r.Conclusion,
		&r.TrustLevel, &r.Source, &r.ConcurrencyGroup, &r.StartedAt, &r.CompletedAt, &r.CreatedAt,
		&r.WorkflowName, &r.WorkflowPath, &r.ProjectSlug, &r.ProjectName)
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	return r, err
}

func (s *Store) Run(ctx context.Context, id uuid.UUID) (Run, error) {
	return scanRun(s.pool.QueryRow(ctx, runSelect+` WHERE r.id = $1`, id))
}

type RunFilter struct {
	ProjectID *uuid.UUID
	Status    string
	Limit     int
	Offset    int
}

func (s *Store) Runs(ctx context.Context, f RunFilter) ([]Run, int, error) {
	limit, offset := page(f.Limit, f.Offset)
	status := f.Status
	rows, err := s.pool.Query(ctx, runSelect+`
		WHERE ($1::uuid IS NULL OR r.project_id = $1)
		  AND ($2 = '' OR r.status = $2)
		ORDER BY r.created_at DESC
		LIMIT $3 OFFSET $4`, f.ProjectID, status, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	var total int
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM workflow_runs
		WHERE ($1::uuid IS NULL OR project_id = $1) AND ($2 = '' OR status = $2)`,
		f.ProjectID, status).Scan(&total); err != nil {
		return nil, 0, err
	}
	return out, total, rows.Err()
}

func (s *Store) JobsForRun(ctx context.Context, runID uuid.UUID) ([]Job, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, run_id, job_key, key, name, status, conclusion, needs, runs_on, image, matrix, env,
		       if_condition, timeout_seconds, continue_on_error, fail_fast, priority, attempt, runner_id,
		       queued_at, started_at, completed_at, lease_expires_at, error, outputs, outputs_spec,
		       environment, environment_url, inputs, services
		FROM jobs WHERE run_id = $1 ORDER BY queued_at, key`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		steps, err := s.StepsForJob(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Steps = steps
	}
	return out, nil
}

func (s *Store) StepsForJob(ctx context.Context, jobID uuid.UUID) ([]Step, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, job_id, number, key, name, status, conclusion, exit_code, started_at,
		       completed_at, spec, log_ref, log_bytes, diagnosis, summary_ref
		FROM steps WHERE job_id = $1 ORDER BY number`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Step{}
	for rows.Next() {
		var st Step
		if err := rows.Scan(&st.ID, &st.JobID, &st.Number, &st.Key, &st.Name, &st.Status,
			&st.Conclusion, &st.ExitCode, &st.StartedAt, &st.CompletedAt, &st.Spec, &st.LogRef,
			&st.LogBytes, &st.Diagnosis, &st.SummaryRef); err != nil {
			return nil, err
		}
		st.HasSummary = st.SummaryRef != ""
		out = append(out, st)
	}
	return out, rows.Err()
}

func scanJob(row pgx.Row) (Job, error) {
	var j Job
	err := row.Scan(&j.ID, &j.RunID, &j.JobKey, &j.Key, &j.Name, &j.Status, &j.Conclusion, &j.Needs,
		&j.RunsOn, &j.Image, &j.Matrix, &j.Env, &j.IfCondition, &j.TimeoutSeconds,
		&j.ContinueOnError, &j.FailFast, &j.Priority, &j.Attempt, &j.RunnerID, &j.QueuedAt,
		&j.StartedAt, &j.CompletedAt, &j.LeaseExpiresAt, &j.Error, &j.Outputs, &j.OutputsSpec,
		&j.Environment, &j.EnvironmentURL, &j.Inputs, &j.Services)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	return j, err
}

func (s *Store) Job(ctx context.Context, id uuid.UUID) (Job, error) {
	j, err := scanJob(s.pool.QueryRow(ctx, `
		SELECT j.id, j.run_id, j.job_key, j.key, j.name, j.status, j.conclusion, j.needs, j.runs_on,
		       j.image, j.matrix, j.env, j.if_condition, j.timeout_seconds, j.continue_on_error,
		       j.fail_fast, j.priority, j.attempt, j.runner_id, j.queued_at, j.started_at, j.completed_at,
		       j.lease_expires_at, j.error, j.outputs, j.outputs_spec, j.environment, j.environment_url,
		       j.inputs, j.services
		FROM jobs j WHERE j.id = $1`, id))
	if err != nil {
		return Job{}, err
	}
	j.Steps, err = s.StepsForJob(ctx, j.ID)
	return j, err
}

// CancelRun marks a run and its non-terminal jobs cancelled.
func (s *Store) CancelRun(ctx context.Context, runID uuid.UUID) (Run, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		UPDATE workflow_runs SET status = 'cancelled', conclusion = 'cancelled', completed_at = now()
		WHERE id = $1 AND status NOT IN ('completed', 'cancelled')`, runID); err != nil {
		return Run{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE jobs SET status = 'cancelled', conclusion = 'cancelled', completed_at = now()
		WHERE run_id = $1 AND status IN ('queued', 'waiting', 'waiting_approval', 'in_progress')`, runID); err != nil {
		return Run{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE deployments SET status = 'failure', completed_at = now()
		WHERE run_id = $1 AND status IN ('queued', 'in_progress')`, runID); err != nil {
		return Run{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE steps SET status = 'cancelled', conclusion = 'cancelled', completed_at = now()
		WHERE status IN ('queued', 'running')
		  AND job_id IN (SELECT id FROM jobs WHERE run_id = $1)`, runID); err != nil {
		return Run{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE job_leases SET released_at = now() WHERE released_at IS NULL
		  AND job_id IN (SELECT id FROM jobs WHERE run_id = $1)`, runID); err != nil {
		return Run{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Run{}, err
	}
	return s.Run(ctx, runID)
}

// PromoteQueuedRuns moves serialized runs (concurrency groups) forward when the
// group is free. When a newer queued run shares a group with an in-progress run
// that opted into cancel-in-progress, the older run is cancelled first.
func (s *Store) PromoteQueuedRuns(ctx context.Context) (int, error) {
	dead, err := s.pool.Query(ctx, `
		UPDATE workflow_runs SET status = 'cancelled', conclusion = 'cancelled', completed_at = now()
		WHERE id IN (
			SELECT o.id FROM workflow_runs o
			JOIN workflow_runs n ON n.project_id = o.project_id
				AND n.concurrency_group = o.concurrency_group
				AND o.concurrency_group <> ''
				AND n.status = 'queued'
				AND (n.created_at, n.id) > (o.created_at, o.id)
			WHERE o.status IN ('queued', 'in_progress')
			  AND o.cancel_in_progress)
		RETURNING id`)
	if err != nil {
		return 0, err
	}
	var deadIDs []uuid.UUID
	for dead.Next() {
		var id uuid.UUID
		if err := dead.Scan(&id); err != nil {
			dead.Close()
			return 0, err
		}
		deadIDs = append(deadIDs, id)
	}
	dead.Close()
	if err := dead.Err(); err != nil {
		return 0, err
	}
	for _, runID := range deadIDs {
		// Jobs of a superseded run were previously left dangling; cancel them
		// and their open deployments alongside the run.
		if _, err := s.pool.Exec(ctx, `
			UPDATE jobs SET status = 'cancelled', conclusion = 'cancelled', completed_at = now()
			WHERE run_id = $1 AND status IN ('queued', 'waiting', 'waiting_approval', 'in_progress')`, runID); err != nil {
			return 0, err
		}
		if _, err := s.pool.Exec(ctx, `
			UPDATE deployments SET status = 'failure', completed_at = now()
			WHERE run_id = $1 AND status IN ('queued', 'in_progress')`, runID); err != nil {
			return 0, err
		}
		if _, err := s.pool.Exec(ctx, `
			UPDATE job_leases SET released_at = now() WHERE released_at IS NULL
			  AND job_id IN (SELECT id FROM jobs WHERE run_id = $1)`, runID); err != nil {
			return 0, err
		}
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE workflow_runs r SET status = 'in_progress', started_at = now()
		WHERE r.status = 'queued'
		  AND NOT EXISTS (
			SELECT 1 FROM workflow_runs o
			WHERE o.project_id = r.project_id
			  AND o.concurrency_group = r.concurrency_group
			  AND o.concurrency_group <> ''
			  AND o.id <> r.id
			  AND o.status IN ('queued', 'in_progress')
			  AND (o.created_at, o.id) < (r.created_at, r.id)
		  )`)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// ApproveRunHold releases a run parked in awaiting_approval back to queued;
// PromoteQueuedRuns then schedules it like any other run.
func (s *Store) ApproveRunHold(ctx context.Context, runID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE workflow_runs SET status = 'queued'
		WHERE id = $1 AND status = 'awaiting_approval'`, runID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("run %s is not awaiting approval", runID)
	}
	return nil
}

// ActiveRuns returns the count of runs that have not reached a terminal status.
func (s *Store) ActiveRuns(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM workflow_runs WHERE status IN ('awaiting_approval','queued','in_progress')`).Scan(&n)
	return n, err
}

func (s *Store) FinishRunIfDone(ctx context.Context, runID uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var pending int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM jobs WHERE run_id = $1 AND status IN ('queued','waiting','waiting_approval','in_progress')`,
		runID).Scan(&pending); err != nil {
		return err
	}
	if pending > 0 {
		return nil
	}
	// Conclusion: failure wins, then cancelled, then success. Skipped-only runs
	// are reported skipped. A failed continue-on-error job keeps its stored
	// conclusion (the UI shows it failed) but, per GitHub semantics, must not
	// fail the run.
	if _, err := tx.Exec(ctx, `
		UPDATE workflow_runs SET
			status = 'completed',
			conclusion = CASE
				WHEN EXISTS (SELECT 1 FROM jobs WHERE run_id = $1
					AND conclusion = 'failure' AND NOT continue_on_error) THEN 'failure'
				WHEN EXISTS (SELECT 1 FROM jobs WHERE run_id = $1 AND conclusion = 'cancelled') THEN 'cancelled'
				WHEN NOT EXISTS (SELECT 1 FROM jobs WHERE run_id = $1
					AND (conclusion = 'success' OR (conclusion = 'failure' AND continue_on_error))) THEN 'skipped'
				ELSE 'success'
			END,
			completed_at = now()
		WHERE id = $1 AND status = 'in_progress'`, runID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// NextRunNumber is exposed for tests and run planning.
func (s *Store) NextRunNumber(ctx context.Context, workflowID uuid.UUID) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(MAX(run_number), 0) + 1 FROM workflow_runs WHERE workflow_id = $1`,
		workflowID).Scan(&n)
	return n, err
}
