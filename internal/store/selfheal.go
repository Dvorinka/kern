package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// runSettings returns the org and project settings documents for a run.
func (s *Store) runSettings(ctx context.Context, runID uuid.UUID) (json.RawMessage, json.RawMessage, error) {
	var orgSettings, projSettings json.RawMessage
	err := s.pool.QueryRow(ctx, `
		SELECT o.settings, p.settings
		FROM workflow_runs r
		JOIN projects p ON p.id = r.project_id
		JOIN orgs o ON o.id = p.org_id
		WHERE r.id = $1`, runID).Scan(&orgSettings, &projSettings)
	return orgSettings, projSettings, err
}

// SelfHealPolicy resolves the remediation ladder for a run: the project's
// self_heal setting wins; "inherit"/missing falls back to the org's; the
// default is "off" — remediation is strictly opt-in.
func (s *Store) SelfHealPolicy(ctx context.Context, runID uuid.UUID) (string, error) {
	orgSettings, projSettings, err := s.runSettings(ctx, runID)
	if err != nil {
		return "off", err
	}
	if v := settingsGet(projSettings, "self_heal"); v != "" && v != "inherit" {
		return v, nil
	}
	if v := settingsGet(orgSettings, "self_heal"); v != "" {
		return v, nil
	}
	return "off", nil
}

// TransientRetryLimit resolves how many automatic requeues a failed job may
// get for transient-class failures (network, oom): the project's
// max_transient_retries setting wins; missing falls back to the org's; the
// default is 1. "0" disables automatic retries.
func (s *Store) TransientRetryLimit(ctx context.Context, runID uuid.UUID) (int, error) {
	orgSettings, projSettings, err := s.runSettings(ctx, runID)
	if err != nil {
		return 0, err
	}
	parse := func(raw json.RawMessage) (int, bool) {
		v := settingsGet(raw, "max_transient_retries")
		if v == "" {
			return 0, false
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 10 {
			return 0, false
		}
		return n, true
	}
	if n, ok := parse(projSettings); ok {
		return n, nil
	}
	if n, ok := parse(orgSettings); ok {
		return n, nil
	}
	return 1, nil
}

func settingsGet(raw json.RawMessage, key string) string {
	var m map[string]string
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	return m[key]
}

// OrgSettings returns the org's settings document.
func (s *Store) OrgSettings(ctx context.Context, orgID uuid.UUID) (json.RawMessage, error) {
	var raw json.RawMessage
	err := s.pool.QueryRow(ctx, `SELECT settings FROM orgs WHERE id = $1`, orgID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return raw, err
}

// ProjectSettings returns the project's settings document.
func (s *Store) ProjectSettings(ctx context.Context, projectID uuid.UUID) (json.RawMessage, error) {
	var raw json.RawMessage
	err := s.pool.QueryRow(ctx, `SELECT settings FROM projects WHERE id = $1`, projectID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return raw, err
}

// PutOrgSetting merges a key into org settings; an empty value deletes the key.
func (s *Store) PutOrgSetting(ctx context.Context, orgID uuid.UUID, key, value string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE orgs SET settings = CASE WHEN $3 = '' THEN settings - $2
			ELSE settings || jsonb_build_object($2::text, $3::text) END
		WHERE id = $1`, orgID, key, value)
	return err
}

// PutProjectSetting merges a key into project settings; an empty value deletes it.
func (s *Store) PutProjectSetting(ctx context.Context, projectID uuid.UUID, key, value string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE projects SET settings = CASE WHEN $3 = '' THEN settings - $2
			ELSE settings || jsonb_build_object($2::text, $3::text) END
		WHERE id = $1`, projectID, key, value)
	return err
}

// ApproveRemediation flips a step's remediation proposal to approved and puts
// the job back on the queue so the fix can run. Steps the job already finished
// keep their results — only non-terminal steps reset, matching lease reaping.
func (s *Store) ApproveRemediation(ctx context.Context, jobID uuid.UUID, stepNumber int) ([]string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	// A finished run can never consume a requeued job — approving here would
	// orphan the job in 'queued' forever. Refuse instead.
	var runStatus string
	if err := tx.QueryRow(ctx, `
		SELECT r.status FROM jobs j JOIN workflow_runs r ON r.id = j.run_id
		WHERE j.id = $1`, jobID).Scan(&runStatus); err != nil {
		return nil, err
	}
	if runStatus == "completed" || runStatus == "cancelled" {
		return nil, fmt.Errorf("run already finished; remediation no longer applicable")
	}
	var fixJSON []byte
	err = tx.QueryRow(ctx, `
		UPDATE steps SET diagnosis = jsonb_set(diagnosis, '{remediation,approved}', 'true')
		WHERE job_id = $1 AND number = $2
		  AND diagnosis->'remediation' IS NOT NULL
		  AND coalesce(diagnosis->'remediation'->>'approved', 'false') <> 'true'
		  AND coalesce(diagnosis->'remediation'->>'applied', 'false') <> 'true'
		  AND coalesce(diagnosis->'remediation'->>'dismissed', 'false') <> 'true'
		RETURNING diagnosis->'remediation'->'fix'`, jobID, stepNumber).Scan(&fixJSON)
	if err != nil {
		return nil, fmt.Errorf("no remediation proposal on step %d", stepNumber)
	}
	var fix []string
	_ = json.Unmarshal(fixJSON, &fix)
	tag, err := tx.Exec(ctx, `
		UPDATE jobs SET status = 'queued', queued_at = now(), runner_id = NULL,
			lease_expires_at = NULL, completed_at = NULL, conclusion = '',
			error = 'requeued: remediation approved'
		WHERE id = $1 AND status <> 'in_progress'`, jobID)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, fmt.Errorf("job is in progress; cannot requeue for remediation")
	}
	if _, err := tx.Exec(ctx, `
		UPDATE steps SET status = 'queued', conclusion = '', started_at = NULL,
			completed_at = NULL, exit_code = NULL
		WHERE job_id = $1 AND status <> 'completed'`, jobID); err != nil {
		return nil, err
	}
	return fix, tx.Commit(ctx)
}

// DismissRemediation marks a proposal dismissed so it stops appearing in the
// attention feed. The step's diagnosis stays; only the offer is withdrawn.
func (s *Store) DismissRemediation(ctx context.Context, jobID uuid.UUID, stepNumber int) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE steps SET diagnosis = jsonb_set(diagnosis, '{remediation,dismissed}', 'true')
		WHERE job_id = $1 AND number = $2
		  AND diagnosis->'remediation' IS NOT NULL
		  AND coalesce(diagnosis->'remediation'->>'applied', 'false') <> 'true'`, jobID, stepNumber)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("no remediation proposal on step %d", stepNumber)
	}
	return nil
}

// ApprovedFixes returns step number → fix commands for remediations an
// operator approved on this job.
func (s *Store) ApprovedFixes(ctx context.Context, jobID uuid.UUID) (map[string][]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT number, diagnosis->'remediation'->'fix'
		FROM steps
		WHERE job_id = $1 AND diagnosis->'remediation'->>'approved' = 'true'`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var n int
		var fix json.RawMessage
		if err := rows.Scan(&n, &fix); err != nil {
			return nil, err
		}
		var cmds []string
		if json.Unmarshal(fix, &cmds) == nil {
			out[fmt.Sprintf("%d", n)] = cmds
		}
	}
	return out, rows.Err()
}
