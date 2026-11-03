package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// SelfHealPolicy resolves the remediation ladder for a run: the project's
// self_heal setting wins; "inherit"/missing falls back to the org's; the
// default is "off" — remediation is strictly opt-in.
func (s *Store) SelfHealPolicy(ctx context.Context, runID uuid.UUID) (string, error) {
	var orgSettings, projSettings json.RawMessage
	err := s.pool.QueryRow(ctx, `
		SELECT o.settings, p.settings
		FROM workflow_runs r
		JOIN projects p ON p.id = r.project_id
		JOIN orgs o ON o.id = p.org_id
		WHERE r.id = $1`, runID).Scan(&orgSettings, &projSettings)
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
func (s *Store) ApproveRemediation(ctx context.Context, jobID uuid.UUID, stepNumber int) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `
		UPDATE steps SET diagnosis = jsonb_set(diagnosis, '{remediation,approved}', 'true')
		WHERE job_id = $1 AND number = $2
		  AND diagnosis->'remediation' IS NOT NULL`, jobID, stepNumber)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("no remediation proposal on step %d", stepNumber)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE jobs SET status = 'queued', queued_at = now(), runner_id = NULL,
			lease_expires_at = NULL, completed_at = NULL, conclusion = '',
			attempt = 0, error = 'requeued: remediation approved'
		WHERE id = $1`, jobID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE steps SET status = 'queued', conclusion = '', started_at = NULL,
			completed_at = NULL, exit_code = NULL
		WHERE job_id = $1 AND status <> 'completed'`, jobID); err != nil {
		return err
	}
	return tx.Commit(ctx)
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
