package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Store) ProjectByID(ctx context.Context, id uuid.UUID) (Project, error) {
	return scanProject(s.pool.QueryRow(ctx, `SELECT `+projectColumns+` FROM projects WHERE id = $1`, id))
}

// ActiveRunIDs lists in_progress runs that have advanceable work (excluding
// runs parked solely on waiting_approval jobs so long-lived approval queues
// cannot starve active runs).
func (s *Store) ActiveRunIDs(ctx context.Context, limit int) ([]uuid.UUID, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.pool.Query(ctx, `
		SELECT r.id FROM workflow_runs r
		WHERE r.status = 'in_progress'
		  AND (
			NOT EXISTS (SELECT 1 FROM jobs j WHERE j.run_id = r.id)
			OR EXISTS (SELECT 1 FROM jobs j WHERE j.run_id = r.id AND j.status <> 'waiting_approval')
		  )
		ORDER BY r.created_at
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// StepByID returns a step together with its job and run identifiers.
func (s *Store) StepByID(ctx context.Context, id uuid.UUID) (Step, uuid.UUID, uuid.UUID, error) {
	var st Step
	var runID uuid.UUID
	err := s.pool.QueryRow(ctx, `
		SELECT s.id, s.job_id, s.number, s.key, s.name, s.status, s.conclusion, s.exit_code,
		       s.started_at, s.completed_at, s.spec, s.log_ref, s.log_bytes, s.diagnosis, s.summary_ref, j.run_id
		FROM steps s JOIN jobs j ON j.id = s.job_id
		WHERE s.id = $1`, id).
		Scan(&st.ID, &st.JobID, &st.Number, &st.Key, &st.Name, &st.Status, &st.Conclusion,
			&st.ExitCode, &st.StartedAt, &st.CompletedAt, &st.Spec, &st.LogRef, &st.LogBytes,
			&st.Diagnosis, &st.SummaryRef, &runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Step{}, uuid.Nil, uuid.Nil, ErrNotFound
	}
	if err != nil {
		return Step{}, uuid.Nil, uuid.Nil, err
	}
	st.HasSummary = st.SummaryRef != ""
	return st, runID, st.JobID, nil
}

// CancelSiblings cancels pending and in-progress jobs that share a job_key
// (matrix fail-fast), releasing their leases, cancelling open steps, and
// failing only deployments whose jobs were actually cancelled.
func (s *Store) CancelSiblings(ctx context.Context, runID uuid.UUID, jobKey string, exceptJobID uuid.UUID) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `
		UPDATE jobs SET status = 'cancelled', conclusion = 'cancelled', completed_at = now(),
			runner_id = NULL, lease_expires_at = NULL, error = 'cancelled by fail-fast'
		WHERE run_id = $1 AND job_key = $2 AND id <> $3
		  AND status IN ('queued', 'waiting', 'waiting_approval', 'in_progress')
		RETURNING id`,
		runID, jobKey, exceptJobID)
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
	if len(ids) == 0 {
		return 0, nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE steps SET status = 'cancelled', conclusion = 'cancelled', completed_at = now()
		WHERE job_id = ANY($1) AND status IN ('queued', 'running')`, ids); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE job_leases SET released_at = now()
		WHERE released_at IS NULL AND job_id = ANY($1)`, ids); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE deployments SET status = 'failure', completed_at = now()
		WHERE job_id = ANY($1) AND status IN ('queued', 'in_progress')`, ids); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return int64(len(ids)), nil
}
