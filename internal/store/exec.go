package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ExecRequest is a queued live-exec session against an in_progress job
// (#132). The leasing runner picks it up on heartbeat, runs the command
// inside the job container, and streams output to the blob store.
type ExecRequest struct {
	ID          uuid.UUID  `json:"id"`
	JobID       uuid.UUID  `json:"job_id"`
	Command     string     `json:"command"`
	Interactive bool       `json:"interactive"`
	State       string     `json:"state"` // pending | running | done | failed | cancelled
	Error       string     `json:"error,omitempty"`
	ExitCode    *int       `json:"exit_code"`
	StdinClosed bool       `json:"stdin_closed"`
	CreatedBy   *uuid.UUID `json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
}

const execCols = `id, job_id, command, interactive, state, error, exit_code,
	stdin_closed, created_by, created_at, started_at, finished_at`

// execColsE is execCols table-qualified for the jobs join — bare names like
// `error` are ambiguous there (jobs has its own error column).
const execColsE = `e.id, e.job_id, e.command, e.interactive, e.state, e.error, e.exit_code,
	e.stdin_closed, e.created_by, e.created_at, e.started_at, e.finished_at`

func scanExec(row pgx.Row) (ExecRequest, error) {
	var e ExecRequest
	err := row.Scan(&e.ID, &e.JobID, &e.Command, &e.Interactive, &e.State,
		&e.Error, &e.ExitCode, &e.StdinClosed, &e.CreatedBy,
		&e.CreatedAt, &e.StartedAt, &e.FinishedAt)
	return e, err
}

// CreateExec queues an exec request for a job. The caller has already
// verified the job is in_progress on a docker-executor runner.
func (s *Store) CreateExec(ctx context.Context, jobID uuid.UUID, command string, interactive bool, createdBy *uuid.UUID) (ExecRequest, error) {
	return scanExec(s.pool.QueryRow(ctx,
		`INSERT INTO exec_requests (job_id, command, interactive, created_by)
		 VALUES ($1, $2, $3, $4) RETURNING `+execCols,
		jobID, command, interactive, createdBy))
}

// Exec returns one exec request.
func (s *Store) Exec(ctx context.Context, id uuid.UUID) (ExecRequest, error) {
	e, err := scanExec(s.pool.QueryRow(ctx,
		`SELECT `+execCols+` FROM exec_requests WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return e, ErrNotFound
	}
	return e, err
}

// PendingExecForRunner lists exec requests awaiting pickup whose jobs are
// leased to this runner. Requests older than 2 minutes are skipped — the
// runner that should have served them is gone or wedged; ExpireStaleExecs
// marks them failed so clients see a terminal state.
func (s *Store) PendingExecForRunner(ctx context.Context, runnerID uuid.UUID) ([]ExecRequest, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+execColsE+`
		   FROM exec_requests e JOIN jobs j ON j.id = e.job_id
		  WHERE j.runner_id = $1 AND e.state = 'pending'
		    AND e.created_at > now() - interval '2 minutes'
		  ORDER BY e.created_at`, runnerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExecRequest
	for rows.Next() {
		e, err := scanExec(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// StartExec flips pending -> running, but only while the job is still
// leased to this runner. ErrNotFound when the request is stale or the
// lease moved.
func (s *Store) StartExec(ctx context.Context, execID, runnerID uuid.UUID) (ExecRequest, error) {
	e, err := scanExec(s.pool.QueryRow(ctx,
		`UPDATE exec_requests SET state = 'running', started_at = now()
		  WHERE id = $1 AND state = 'pending'
		    AND job_id IN (SELECT id FROM jobs WHERE runner_id = $2)
		  RETURNING `+execCols, execID, runnerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return e, ErrNotFound
	}
	return e, err
}

// FinishExec records the terminal state for a session the runner served.
// A non-empty error or nil exit code stores 'failed'.
func (s *Store) FinishExec(ctx context.Context, execID, runnerID uuid.UUID, exitCode *int, errText string) error {
	state := "done"
	if errText != "" || exitCode == nil {
		state = "failed"
	}
	_, err := scanExec(s.pool.QueryRow(ctx,
		`UPDATE exec_requests SET state = $3, error = $4, exit_code = $5,
		        finished_at = now(), stdin_closed = true
		  WHERE id = $1 AND state = 'running'
		    AND job_id IN (SELECT id FROM jobs WHERE runner_id = $2)
		  RETURNING `+execCols, execID, runnerID, state, errText, exitCode))
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// CloseExecStdin marks the stdin pipe closed so the runner stops polling.
func (s *Store) CloseExecStdin(ctx context.Context, execID uuid.UUID) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE exec_requests SET stdin_closed = true WHERE id = $1`, execID)
	return err
}

// FailExecsForJob terminates every live exec for a job — called when the
// job finishes so no session outlives its container.
func (s *Store) FailExecsForJob(ctx context.Context, jobID uuid.UUID, reason string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE exec_requests SET state = 'failed', error = $2, finished_at = now()
		  WHERE job_id = $1 AND state IN ('pending', 'running')`, jobID, reason)
	return err
}

// ExpireStaleExecs fails pending requests nobody picked up in time.
func (s *Store) ExpireStaleExecs(ctx context.Context) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE exec_requests SET state = 'failed', error = 'no runner picked up the exec request',
		        finished_at = now()
		  WHERE state = 'pending' AND created_at < now() - interval '2 minutes'`)
	return err
}
