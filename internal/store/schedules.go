package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// ScheduledWorkflow is a workflow whose latest valid version declares a
// schedule trigger.
type ScheduledWorkflow struct {
	Project    Project
	WorkflowID uuid.UUID
	VersionID  uuid.UUID
	CommitSHA  string
	Schedules  []string // cron expressions
}

// ScheduledWorkflows returns every workflow whose newest valid version carries
// a schedule trigger. Triggers live in plan JSON at parsed->'triggers'.
func (s *Store) ScheduledWorkflows(ctx context.Context) ([]ScheduledWorkflow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (w.id)
		       w.id, v.id, v.commit_sha, v.parsed->'triggers'->'schedule',
		       p.id, p.org_id, p.slug, p.name, p.provider, p.repo_full_name, p.repo_url,
		       p.default_branch, p.local_path, p.created_at, p.updated_at
		FROM workflows w
		JOIN workflow_versions v ON v.workflow_id = w.id AND v.valid
		JOIN projects p ON p.id = w.project_id
		WHERE v.parsed->'triggers' ? 'schedule' AND NOT w.disabled
		ORDER BY w.id, v.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ScheduledWorkflow{}
	for rows.Next() {
		var sw ScheduledWorkflow
		var raw []byte
		p := &sw.Project
		if err := rows.Scan(&sw.WorkflowID, &sw.VersionID, &sw.CommitSHA, &raw,
			&p.ID, &p.OrgID, &p.Slug, &p.Name, &p.Provider, &p.RepoFullName, &p.RepoURL,
			&p.DefaultBranch, &p.LocalPath, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		var entries []struct {
			Cron string `json:"cron"`
		}
		if err := json.Unmarshal(raw, &entries); err != nil {
			continue
		}
		for _, e := range entries {
			if e.Cron != "" {
				sw.Schedules = append(sw.Schedules, e.Cron)
			}
		}
		out = append(out, sw)
	}
	return out, rows.Err()
}

// HasScheduledRun reports whether a schedule run already exists for this version
// minted at or after the given cron instant — the dedupe guard.
func (s *Store) HasScheduledRun(ctx context.Context, versionID uuid.UUID, instant time.Time) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM workflow_runs
			WHERE workflow_version_id = $1 AND event = 'schedule' AND created_at >= $2)`,
		versionID, instant).Scan(&exists)
	return exists, err
}
