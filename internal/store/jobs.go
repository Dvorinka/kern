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

// ClaimedJob is everything a runner needs to execute a job. It deliberately excludes
// anything the runner must not know (secrets are delivered separately, per job).
type ClaimedJob struct {
	Job             Job            `json:"job"`
	Run             Run            `json:"run"`
	Project         Project        `json:"-"`
	StepSpecs       []StepSeed     `json:"-"`
	LeaseExpiresAt  time.Time      `json:"lease_expires_at"`
	Attempt         int            `json:"attempt"`
	Source          map[string]any `json:"-"`
	TimeoutSeconds  int            `json:"timeout_seconds"`
	WorkflowContent string         `json:"-"`
}

// ClaimJob atomically takes one queued job that the runner is compatible with.
// Returns (nil, nil) when the queue is empty.
func (s *Store) ClaimJob(ctx context.Context, runnerID uuid.UUID, lease time.Duration) (*ClaimedJob, error) {
	leaseSeconds := int(lease.Seconds())
	var jobID uuid.UUID
	err := s.pool.QueryRow(ctx, `
		WITH me AS (
			SELECT * FROM runners WHERE id = $1
		), fleet AS MATERIALIZED (
			-- Per-runner in-progress counts, computed once per claim instead of
			-- once per (candidate job × peer runner) pair in the yield check.
			SELECT r.id, r.labels, r.executor,
				(SELECT count(*) FROM jobs a
				 WHERE a.runner_id = r.id AND a.status = 'in_progress') AS load
			FROM runners r, me
			WHERE r.org_id = me.org_id
			  AND r.status = 'online'
			  AND NOT r.draining
		), candidate AS (
			SELECT j.id
			FROM jobs j
			JOIN workflow_runs r ON r.id = j.run_id
			CROSS JOIN me
			JOIN fleet me_f ON me_f.id = me.id
			WHERE j.status = 'queued'
			  AND r.status = 'in_progress'
			  AND j.runs_on <@ (me_f.labels || ARRAY['self-hosted', 'kern'])
			  -- Host-exec runners have no isolation: fork/pull_request trust
			  -- tiers (untrusted code) may never land on them. Same boundary
			  -- as secrets eligibility.
			  AND (me_f.executor <> 'host' OR r.trust_level NOT IN ('fork', 'pull_request'))
			  -- Docker-in-job work prefers runners advertising kern-docker
			  -- (host executors with a working daemon). It is a preference,
			  -- not a gate: untrusted runs can't use host executors anyway,
			  -- and with no capable runner online anyone may try — the job
			  -- fails on the missing daemon exactly as it would today.
			  AND (
				NOT j.needs_docker
				OR 'kern-docker' = ANY(me_f.labels)
				OR r.trust_level IN ('fork', 'pull_request')
				OR NOT EXISTS (
					SELECT 1 FROM fleet h
					WHERE 'kern-docker' = ANY(h.labels)
					  AND j.runs_on <@ (h.labels || ARRAY['self-hosted', 'kern'])
				)
			  )
			  -- Least-loaded placement: yield to any eligible runner carrying
			  -- fewer in-progress jobs than this one.
			  AND NOT EXISTS (
				SELECT 1 FROM fleet o
				WHERE o.id <> me_f.id
				  AND j.runs_on <@ (o.labels || ARRAY['self-hosted', 'kern'])
				  AND o.load < me_f.load
			  )
			ORDER BY j.priority DESC, j.queued_at
			FOR UPDATE OF j SKIP LOCKED
			LIMIT 1
		), claimed AS (
			UPDATE jobs j SET
				status = 'in_progress',
				runner_id = $1,
				started_at = now(),
				attempt = j.attempt + 1,
				lease_expires_at = now() + make_interval(secs => $2),
				error = ''
			WHERE j.id IN (SELECT id FROM candidate)
			RETURNING j.id
		)
		INSERT INTO job_leases (job_id, runner_id, expires_at)
		SELECT id, $1, now() + make_interval(secs => $2) FROM claimed
		RETURNING job_id`, runnerID, leaseSeconds).Scan(&jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim job: %w", err)
	}
	// A claimed deployment job is now actually deploying.
	if _, err := s.pool.Exec(ctx, `
		UPDATE deployments SET status = 'in_progress'
		WHERE job_id = $1 AND status = 'queued'`, jobID); err != nil {
		return nil, fmt.Errorf("claim deployment: %w", err)
	}

	var out ClaimedJob
	var runID, versionID uuid.UUID
	err = s.pool.QueryRow(ctx, `
		SELECT j.id, j.run_id, j.job_key, j.key, j.name, j.status, j.conclusion, j.needs, j.runs_on,
		       j.image, j.matrix, j.env, j.if_condition, j.timeout_seconds, j.continue_on_error,
		       j.fail_fast, j.priority, j.attempt, j.runner_id, j.queued_at, j.started_at, j.completed_at,
		       j.lease_expires_at, j.error, j.outputs, j.outputs_spec, j.environment, j.environment_url,
		       j.inputs, j.services, j.permissions, j.classification,
		       r.id, r.project_id, r.workflow_id, r.workflow_version_id, r.run_number, r.event,
		       r.ref, r.sha, r.commit_message, r.actor, r.status, r.conclusion, r.trust_level,
		       r.source, r.concurrency_group, r.started_at, r.completed_at, r.created_at,
		       w.name, w.path, p.slug, p.name,
		       v.content
		FROM jobs j
		JOIN workflow_runs r ON r.id = j.run_id
		JOIN workflows w ON w.id = r.workflow_id
		JOIN projects p ON p.id = r.project_id
		JOIN workflow_versions v ON v.id = r.workflow_version_id
		WHERE j.id = $1`, jobID).
		Scan(&out.Job.ID, &out.Job.RunID, &out.Job.JobKey, &out.Job.Key, &out.Job.Name, &out.Job.Status,
			&out.Job.Conclusion, &out.Job.Needs, &out.Job.RunsOn, &out.Job.Image, &out.Job.Matrix,
			&out.Job.Env, &out.Job.IfCondition, &out.Job.TimeoutSeconds, &out.Job.ContinueOnError,
			&out.Job.FailFast, &out.Job.Priority, &out.Job.Attempt, &out.Job.RunnerID, &out.Job.QueuedAt,
			&out.Job.StartedAt, &out.Job.CompletedAt, &out.Job.LeaseExpiresAt, &out.Job.Error,
			&out.Job.Outputs, &out.Job.OutputsSpec, &out.Job.Environment, &out.Job.EnvironmentURL,
			&out.Job.Inputs, &out.Job.Services, &out.Job.Permissions, &out.Job.Classification,
			&out.Run.ID, &out.Run.ProjectID, &runID, &versionID, &out.Run.RunNumber, &out.Run.Event,
			&out.Run.Ref, &out.Run.SHA, &out.Run.CommitMessage, &out.Run.Actor, &out.Run.Status,
			&out.Run.Conclusion, &out.Run.TrustLevel, &out.Run.Source, &out.Run.ConcurrencyGroup,
			&out.Run.StartedAt, &out.Run.CompletedAt, &out.Run.CreatedAt,
			&out.Run.WorkflowName, &out.Run.WorkflowPath, &out.Run.ProjectSlug, &out.Run.ProjectName,
			&out.WorkflowContent)
	if err != nil {
		return nil, fmt.Errorf("load claimed job: %w", err)
	}
	out.Run.WorkflowID = runID
	out.Run.WorkflowVersionID = versionID
	out.Attempt = out.Job.Attempt
	out.TimeoutSeconds = out.Job.TimeoutSeconds
	if out.Job.LeaseExpiresAt != nil {
		out.LeaseExpiresAt = *out.Job.LeaseExpiresAt
	}
	if err := json.Unmarshal(out.Run.Source, &out.Source); err != nil {
		out.Source = map[string]any{}
	}
	// Step specs are reconstructed from the persisted steps so the runner executes
	// exactly the steps the planner created.
	steps, err := s.StepsForJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	for i, st := range steps {
		out.Job.Steps = append(out.Job.Steps, st)
		out.StepSpecs = append(out.StepSpecs, StepSeed{Key: fmt.Sprintf("%d", i+1), Name: st.Name})
	}
	out.Project, _ = s.projectByID(ctx, out.Run.ProjectID)
	return &out, nil
}

func (s *Store) projectByID(ctx context.Context, id uuid.UUID) (Project, error) {
	return scanProject(s.pool.QueryRow(ctx, `SELECT `+projectColumns+` FROM projects WHERE id = $1`, id))
}

// RenewLease extends the job lease and records a runner heartbeat.
func (s *Store) RenewLease(ctx context.Context, runnerID, jobID uuid.UUID, lease time.Duration) (bool, error) {
	var expires time.Time
	err := s.pool.QueryRow(ctx, `
		UPDATE jobs SET lease_expires_at = now() + make_interval(secs => $3)
		WHERE id = $1 AND runner_id = $2 AND status = 'in_progress'
		RETURNING lease_expires_at`, jobID, runnerID, int(lease.Seconds())).Scan(&expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, _ = s.pool.Exec(ctx, `UPDATE job_leases SET expires_at = $1 WHERE job_id = $2 AND released_at IS NULL`, expires, jobID)
	_, err = s.pool.Exec(ctx, `UPDATE runners SET last_heartbeat_at = now() WHERE id = $1`, runnerID)
	return true, err
}

// Heartbeat records liveness and the latest utilization snapshot for a runner.
// Version and disk capacity refresh here too — a token-mode runner never
// re-registers, so register-time values would go stale forever; a stale
// version made the auto-update offer loop on the same binary.
func (s *Store) Heartbeat(ctx context.Context, runnerID uuid.UUID, m HostMetrics) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE runners SET last_heartbeat_at = now(),
			status = CASE WHEN draining THEN 'draining' ELSE 'online' END,
			cpu_percent = $2, mem_used_bytes = $3, disk_free_bytes = $4, metrics_at = now(),
			version = COALESCE(NULLIF($5, ''), version),
			disk_bytes = COALESCE($6, disk_bytes)
		WHERE id = $1`, runnerID, m.CPUPercent, m.MemUsedBytes, m.DiskFreeBytes,
		m.Version, m.DiskBytes)
	return err
}

func (s *Store) StartStep(ctx context.Context, jobID uuid.UUID, number int) (Step, error) {
	var st Step
	err := s.pool.QueryRow(ctx, `
		UPDATE steps SET status = 'running', started_at = now()
		WHERE job_id = $1 AND number = $2
		RETURNING id, job_id, number, key, name, status, conclusion, exit_code, started_at,
		          completed_at, spec, log_ref, log_bytes, diagnosis, summary_ref`, jobID, number).
		Scan(&st.ID, &st.JobID, &st.Number, &st.Key, &st.Name, &st.Status, &st.Conclusion,
			&st.ExitCode, &st.StartedAt, &st.CompletedAt, &st.Spec, &st.LogRef, &st.LogBytes,
			&st.Diagnosis, &st.SummaryRef)
	st.HasSummary = st.SummaryRef != ""
	return st, err
}

func (s *Store) FinishStep(ctx context.Context, jobID uuid.UUID, number int, conclusion string, exitCode *int, diagnosis, annotations json.RawMessage) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE steps SET status = CASE WHEN $3 = 'skipped' THEN 'skipped' ELSE 'completed' END,
			conclusion = $3, exit_code = $4, completed_at = now(), diagnosis = $5, annotations = $6
		WHERE job_id = $1 AND number = $2`, jobID, number, conclusion, exitCode, diagnosis, annotations)
	return err
}

// SetStepSummary records the blob reference of a step's markdown summary.
func (s *Store) SetStepSummary(ctx context.Context, jobID uuid.UUID, number int, ref string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE steps SET summary_ref = $3
		WHERE job_id = $1 AND number = $2`, jobID, number, ref)
	return err
}

// AppendStepLog records a log chunk size and reference. Chunks themselves live in blob storage.
func (s *Store) AppendStepLog(ctx context.Context, jobID uuid.UUID, number int, ref string, size int) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE steps SET log_ref = $3, log_bytes = log_bytes + $4
		WHERE job_id = $1 AND number = $2`, jobID, number, ref, size)
	return err
}

func (s *Store) StepLogRef(ctx context.Context, jobID uuid.UUID, number int) (string, int64, error) {
	var ref string
	var size int64
	err := s.pool.QueryRow(ctx, `
		SELECT log_ref, log_bytes FROM steps WHERE job_id = $1 AND number = $2`, jobID, number).Scan(&ref, &size)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, ErrNotFound
	}
	return ref, size, err
}

// FinishJob records the terminal state of a job and releases its lease.
// classification is the rolled-up failure class (” for non-failures).
func (s *Store) FinishJob(ctx context.Context, runnerID, jobID uuid.UUID, conclusion, errMsg string, outputs map[string]string, metrics json.RawMessage, classification string) (bool, error) {
	outputsJSON, err := json.Marshal(outputs)
	if err != nil {
		return false, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	err = tx.QueryRow(ctx, `
		UPDATE jobs SET
			status = 'completed',
			conclusion = $3,
			completed_at = now(),
			lease_expires_at = NULL,
			error = $4,
			outputs = $5,
			metrics = COALESCE($6, metrics),
			classification = $7
		WHERE id = $1 AND runner_id = $2 AND status = 'in_progress'
		RETURNING id`, jobID, runnerID, conclusion, errMsg, outputsJSON, metrics, classification).Scan(&jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE job_leases SET released_at = now() WHERE job_id = $1 AND released_at IS NULL`, jobID); err != nil {
		return false, err
	}
	depStatus := "failure"
	if conclusion == "success" || conclusion == "skipped" {
		depStatus = "success"
	}
	if _, err := tx.Exec(ctx, `
		UPDATE deployments SET status = $2, completed_at = now()
		WHERE job_id = $1 AND status IN ('queued', 'in_progress')`, jobID, depStatus); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// JobFailureKinds returns the distinct diagnosis kinds of the job's failed
// steps, ordered by the step that produced each kind — kinds[0] is the first
// failed step's class, used both for transient-retry decisions and for the
// job's rolled-up classification. Empty when no step has a recorded failure
// diagnosis (e.g. prepare-phase failures, #209).
func (s *Store) JobFailureKinds(ctx context.Context, jobID uuid.UUID) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT diagnosis->>'kind' FROM steps
		WHERE job_id = $1 AND status = 'completed' AND conclusion = 'failure'
		  AND diagnosis IS NOT NULL
		GROUP BY 1 ORDER BY min(number)`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var kinds []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		kinds = append(kinds, k)
	}
	return kinds, rows.Err()
}

// RequeueJob puts an in-progress job back on the queue for another claim.
// The caller decides eligibility (transient diagnosis, attempt budget);
// attempt increments on the next claim, matching the lease-reaper.
func (s *Store) RequeueJob(ctx context.Context, runnerID, jobID uuid.UUID, reason string) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `
		UPDATE jobs SET status = 'queued', queued_at = now(), runner_id = NULL,
			lease_expires_at = NULL, started_at = NULL, completed_at = NULL,
			conclusion = '', error = $3
		WHERE id = $1 AND runner_id = $2 AND status = 'in_progress'`,
		jobID, runnerID, reason)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE job_leases SET released_at = now() WHERE job_id = $1 AND released_at IS NULL`, jobID); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE steps SET status = 'queued', conclusion = '', started_at = NULL,
			completed_at = NULL, exit_code = NULL
		WHERE job_id = $1 AND status <> 'completed'`, jobID); err != nil {
		return false, err
	}
	// A requeued job returns its deployment to queued, matching lease reaping.
	if _, err := tx.Exec(ctx, `
		UPDATE deployments SET status = 'queued', completed_at = NULL
		WHERE job_id = $1 AND status IN ('queued', 'in_progress')`, jobID); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// ReapExpiredLeases requeues jobs whose runner stopped reporting. Returns affected run IDs.
func (s *Store) ReapExpiredLeases(ctx context.Context, maxAttempts int) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx, `
		WITH expired AS (
			SELECT j.id, j.attempt, j.run_id
			FROM jobs j
			WHERE j.status = 'in_progress' AND j.lease_expires_at < now()
			FOR UPDATE SKIP LOCKED
		), updated AS (
			UPDATE jobs j SET
				status = CASE WHEN e.attempt >= $1 THEN 'completed' ELSE 'queued' END,
				conclusion = CASE WHEN e.attempt >= $1 THEN 'failure' ELSE '' END,
				classification = CASE WHEN e.attempt >= $1 THEN 'infra' ELSE '' END,
				error = CASE WHEN e.attempt >= $1
					THEN 'lease expired: runner stopped reporting'
					ELSE 'lease expired: requeued' END,
				runner_id = NULL,
				lease_expires_at = NULL,
				started_at = CASE WHEN e.attempt >= $1 THEN j.started_at ELSE NULL END,
				completed_at = CASE WHEN e.attempt >= $1 THEN now() ELSE NULL END
			FROM expired e
			WHERE j.id = e.id
			RETURNING j.id, j.run_id
		)
		SELECT id, run_id FROM updated`, maxAttempts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runSet := map[uuid.UUID]struct{}{}
	var jobIDs []uuid.UUID
	for rows.Next() {
		var jobID, runID uuid.UUID
		if err := rows.Scan(&jobID, &runID); err != nil {
			return nil, err
		}
		jobIDs = append(jobIDs, jobID)
		runSet[runID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, jobID := range jobIDs {
		_, _ = s.pool.Exec(ctx, `
			UPDATE job_leases SET released_at = now() WHERE job_id = $1 AND released_at IS NULL`, jobID)
		_, _ = s.pool.Exec(ctx, `
			UPDATE steps SET status = 'queued', conclusion = '', started_at = NULL,
				completed_at = NULL, exit_code = NULL
			WHERE job_id = $1 AND status <> 'completed'`, jobID)
		// Requeued jobs return their deployment to queued; jobs that exhausted
		// attempts fail it.
		_, _ = s.pool.Exec(ctx, `
			UPDATE deployments d SET
				status = CASE WHEN j.status = 'completed' THEN 'failure' ELSE 'queued' END,
				completed_at = CASE WHEN j.status = 'completed' THEN now() ELSE NULL END
			FROM jobs j
			WHERE j.id = $1 AND d.job_id = j.id AND d.status IN ('queued', 'in_progress')`, jobID)
	}
	out := make([]uuid.UUID, 0, len(runSet))
	for id := range runSet {
		out = append(out, id)
	}
	return out, nil
}

// TimeoutOverdueJobs fails in-progress jobs that exceeded their
// timeout_seconds ceiling. This is a belt-and-braces watchdog: the runner
// enforces the same timeout via context, so this only fires when a runner
// keeps renewing the lease without enforcing it.
func (s *Store) TimeoutOverdueJobs(ctx context.Context) (int64, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE jobs SET status = 'completed', conclusion = 'failure',
			classification = 'timeout',
			error = 'job exceeded timeout-minutes', completed_at = now(),
			lease_expires_at = NULL
		WHERE status = 'in_progress'
		  AND started_at IS NOT NULL
		  AND started_at + make_interval(secs => timeout_seconds) < now()
		RETURNING id`)
	if err != nil {
		return 0, err
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	s.failDeploymentsForJobs(ctx, ids)
	return int64(len(ids)), nil
}

// PruneStepBlobs clears log and summary references on steps of runs completed
// before the cutoff, returning the blob keys the caller should delete.
func (s *Store) PruneStepBlobs(ctx context.Context, olderThan time.Duration) ([]string, error) {
	secs := int(olderThan.Seconds())
	var refs []string
	for _, q := range []struct{ col, set string }{
		{"log_ref", "log_ref = '', log_bytes = 0"},
		{"summary_ref", "summary_ref = ''"},
	} {
		rows, err := s.pool.Query(ctx, fmt.Sprintf(`
			UPDATE steps s SET %s
			FROM jobs j JOIN workflow_runs r ON r.id = j.run_id
			WHERE s.job_id = j.id AND s.%s <> ''
			  AND r.completed_at IS NOT NULL
			  AND r.completed_at < now() - make_interval(secs => $1)
			RETURNING s.%s`, q.set, q.col, q.col), secs)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var ref string
			if err := rows.Scan(&ref); err != nil {
				rows.Close()
				return nil, err
			}
			refs = append(refs, ref)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return refs, nil
}

// OfflineRunner is a runner that just flipped offline in a sweep — enough
// identity to alert on the transition.
type OfflineRunner struct {
	ID    uuid.UUID `json:"id"`
	OrgID uuid.UUID `json:"org_id"`
	Name  string    `json:"name"`
}

// MarkStaleRunnersOffline flips runners that missed heartbeats and returns
// which ones transitioned, so the caller can alert on the edge rather than
// the state. Ephemeral runners that stay offline past the grace window are
// deleted outright — their credentials are already dead and the row is
// only fleet clutter.
func (s *Store) MarkStaleRunnersOffline(ctx context.Context, timeout time.Duration) ([]OfflineRunner, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE runners SET status = 'offline'
		WHERE status IN ('online', 'draining')
		  AND (last_heartbeat_at IS NULL OR last_heartbeat_at < now() - make_interval(secs => $1))
		RETURNING id, org_id, name`,
		int(timeout.Seconds()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OfflineRunner
	for rows.Next() {
		var r OfflineRunner
		if err := rows.Scan(&r.ID, &r.OrgID, &r.Name); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	_, _ = s.pool.Exec(ctx, `
		DELETE FROM runners
		WHERE ephemeral AND status = 'offline'
		  AND (last_heartbeat_at IS NULL OR last_heartbeat_at < now() - interval '24 hours')`)
	return out, nil
}

// CancelOrphanedJobs cancels queued/waiting jobs whose run already reached a
// terminal state. ClaimJob requires an in_progress run, so these rows can
// never be claimed — left alone they inflate the queue count forever.
func (s *Store) CancelOrphanedJobs(ctx context.Context) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `
		UPDATE jobs j SET status = 'cancelled', conclusion = 'cancelled',
			completed_at = now(), error = 'run already finished'
		FROM workflow_runs r
		WHERE j.run_id = r.id
		  AND j.status IN ('queued', 'waiting', 'waiting_approval')
		  AND r.status IN ('completed', 'cancelled')
		RETURNING j.id`)
	if err != nil {
		return 0, err
	}
	var jobIDs []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		jobIDs = append(jobIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, id := range jobIDs {
		if _, err := tx.Exec(ctx, `
			UPDATE steps SET status = 'cancelled', conclusion = 'cancelled', completed_at = now()
			WHERE job_id = $1 AND status <> 'completed'`, id); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE deployments SET status = 'failure', completed_at = now()
			WHERE job_id = $1 AND status IN ('queued', 'in_progress')`, id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return int64(len(jobIDs)), nil
}

func (s *Store) SetJobQueued(ctx context.Context, jobID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE jobs SET status = 'queued', queued_at = now() WHERE id = $1 AND status = 'waiting'`, jobID)
	return err
}

func (s *Store) SetJobSkipped(ctx context.Context, jobID uuid.UUID, reason string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		UPDATE jobs SET status = 'skipped', conclusion = 'skipped', completed_at = now(),
			error = $2
		WHERE id = $1 AND status = 'waiting'`, jobID, reason); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE steps SET status = 'skipped', conclusion = 'skipped', completed_at = now()
		WHERE job_id = $1 AND status = 'queued'`, jobID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// UnmatchedJob is a queued job that no online runner can ever claim.
type UnmatchedJob struct {
	JobID  uuid.UUID `json:"job_id"`
	RunID  uuid.UUID `json:"run_id"`
	RunsOn []string  `json:"runs_on"`
}

// UnmatchedQueuedJobs lists queued jobs that would starve silently: the
// label subset check from ClaimJob finds no online, non-draining runner in
// the job's org whose labels cover runs_on (plus the implicit
// self-hosted/kern pair), or the only matches are host executors the run's
// trust tier forbids. Capacity is deliberately not part of the predicate —
// a job matching a busy runner is queueing, not starvation.
func (s *Store) UnmatchedQueuedJobs(ctx context.Context) ([]UnmatchedJob, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT j.id, j.run_id, j.runs_on
		FROM jobs j
		JOIN workflow_runs r ON r.id = j.run_id
		JOIN projects p ON p.id = r.project_id
		WHERE j.status = 'queued'
		  AND r.status IN ('queued', 'in_progress')
		  AND NOT EXISTS (
			SELECT 1 FROM runners rn
			WHERE rn.org_id = p.org_id
			  AND rn.status = 'online'
			  AND NOT rn.draining
			  AND j.runs_on <@ (rn.labels || ARRAY['self-hosted', 'kern'])
			  AND (rn.executor <> 'host' OR r.trust_level NOT IN ('fork', 'pull_request'))
		  )
		ORDER BY j.queued_at`)
	if err != nil {
		return nil, fmt.Errorf("unmatched queued jobs: %w", err)
	}
	defer rows.Close()
	var out []UnmatchedJob
	for rows.Next() {
		var u UnmatchedJob
		if err := rows.Scan(&u.JobID, &u.RunID, &u.RunsOn); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// JobRunID resolves the run for a job (used by log streaming and cancellation checks).
func (s *Store) JobRunID(ctx context.Context, jobID uuid.UUID) (uuid.UUID, string, error) {
	var runID uuid.UUID
	var status string
	err := s.pool.QueryRow(ctx, `SELECT run_id, status FROM jobs WHERE id = $1`, jobID).Scan(&runID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, "", ErrNotFound
	}
	return runID, status, err
}

// JobSecretsMap returns the job's reusable-caller secrets spec: nil means
// deliver the full eligible set (normal jobs, 'secrets: inherit', and rows
// predating the column), an empty non-nil map means none, otherwise
// called-name -> caller expression resolved by the API at fetch time.
func (s *Store) JobSecretsMap(ctx context.Context, jobID uuid.UUID) (map[string]string, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT secrets_map FROM jobs WHERE id = $1`, jobID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("job secrets map: %w", err)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("job secrets map: %w", err)
	}
	// jsonb 'null' unmarshals to a nil map — same "all secrets" verdict as a
	// missing column; '{}' yields an empty non-nil map = none.
	return m, nil
}

// RunStatus is a lightweight status read.
func (s *Store) RunStatus(ctx context.Context, runID uuid.UUID) (string, string, error) {
	var status, conclusion string
	err := s.pool.QueryRow(ctx, `SELECT status, conclusion FROM workflow_runs WHERE id = $1`, runID).Scan(&status, &conclusion)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	}
	return status, conclusion, err
}

// LogSearchStep is one step whose log blob is a search candidate — enough
// context to render a hit without a second round trip.
type LogSearchStep struct {
	StepID    uuid.UUID
	Number    int
	Name      string
	LogRef    string
	JobKey    string
	JobName   string
	RunID     uuid.UUID
	RunNumber int
	Workflow  string
	Project   string
}

// SearchLogSteps lists steps that hold a log blob, newest runs first — the
// candidate set for the log-search endpoint. project and run scope the
// scan; limit caps candidates so a full retention corpus cannot pin the
// search loop (#135).
func (s *Store) SearchLogSteps(ctx context.Context, orgID uuid.UUID, project string, runID uuid.UUID, limit int) ([]LogSearchStep, error) {
	q := `SELECT s.id, s.number, s.name, s.log_ref, j.key, j.name,
		       r.id, r.run_number, w.name, p.slug
		FROM steps s
		JOIN jobs j ON j.id = s.job_id
		JOIN workflow_runs r ON r.id = j.run_id
		JOIN workflows w ON w.id = r.workflow_id
		JOIN projects p ON p.id = r.project_id
		WHERE p.org_id = $1 AND s.log_ref <> ''`
	args := []any{orgID}
	n := 2
	if project != "" {
		q += fmt.Sprintf(" AND p.slug = $%d", n)
		args = append(args, project)
		n++
	}
	if runID != uuid.Nil {
		q += fmt.Sprintf(" AND r.id = $%d", n)
		args = append(args, runID)
		n++
	}
	q += fmt.Sprintf(" ORDER BY r.created_at DESC, j.key, s.number LIMIT $%d", n)
	args = append(args, limit)
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LogSearchStep
	for rows.Next() {
		var st LogSearchStep
		if err := rows.Scan(&st.StepID, &st.Number, &st.Name, &st.LogRef,
			&st.JobKey, &st.JobName, &st.RunID, &st.RunNumber,
			&st.Workflow, &st.Project); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}
