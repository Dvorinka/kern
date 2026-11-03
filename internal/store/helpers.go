package store

import (
	"context"

	"github.com/google/uuid"
)

func (s *Store) ProjectByID(ctx context.Context, id uuid.UUID) (Project, error) {
	return scanProject(s.pool.QueryRow(ctx, `SELECT `+projectColumns+` FROM projects WHERE id = $1`, id))
}

// ActiveRunIDs lists runs that still have work to advance.
func (s *Store) ActiveRunIDs(ctx context.Context, limit int) ([]uuid.UUID, error) {
	limit, _ = page(limit, 0)
	rows, err := s.pool.Query(ctx, `
		SELECT id FROM workflow_runs
		WHERE status = 'in_progress'
		ORDER BY created_at
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
		       s.started_at, s.completed_at, s.spec, s.log_ref, s.log_bytes, j.run_id
		FROM steps s JOIN jobs j ON j.id = s.job_id
		WHERE s.id = $1`, id).
		Scan(&st.ID, &st.JobID, &st.Number, &st.Key, &st.Name, &st.Status, &st.Conclusion,
			&st.ExitCode, &st.StartedAt, &st.CompletedAt, &st.Spec, &st.LogRef, &st.LogBytes, &runID)
	if err != nil {
		return Step{}, uuid.Nil, uuid.Nil, err
	}
	return st, runID, st.JobID, nil
}

// CancelSiblings cancels pending jobs that share a job_key (matrix fail-fast).
func (s *Store) CancelSiblings(ctx context.Context, runID uuid.UUID, jobKey string, exceptJobID uuid.UUID) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE jobs SET status = 'cancelled', conclusion = 'cancelled', completed_at = now(),
			error = 'cancelled by fail-fast'
		WHERE run_id = $1 AND job_key = $2 AND id <> $3 AND status IN ('queued', 'waiting')`,
		runID, jobKey, exceptJobID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
