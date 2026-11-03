package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Store) UpsertSecret(ctx context.Context, orgID uuid.UUID, projectID *uuid.UUID, name string, ciphertext, nonce []byte) error {
	if projectID == nil {
		_, err := s.pool.Exec(ctx, `
			INSERT INTO secrets (org_id, name, ciphertext, nonce) VALUES ($1, $2, $3, $4)
			ON CONFLICT (org_id, name) WHERE project_id IS NULL AND environment_id IS NULL
			DO UPDATE SET ciphertext = EXCLUDED.ciphertext, nonce = EXCLUDED.nonce, updated_at = now()`,
			orgID, name, ciphertext, nonce)
		return err
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO secrets (org_id, project_id, name, ciphertext, nonce) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (project_id, name) WHERE project_id IS NOT NULL AND environment_id IS NULL
		DO UPDATE SET ciphertext = EXCLUDED.ciphertext, nonce = EXCLUDED.nonce, updated_at = now()`,
		orgID, *projectID, name, ciphertext, nonce)
	return err
}

func (s *Store) Secrets(ctx context.Context, orgID uuid.UUID, projectID *uuid.UUID) ([]Secret, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, org_id, project_id, name, ciphertext, nonce, created_at, updated_at
		FROM secrets
		WHERE org_id = $1 AND ($2::uuid IS NULL OR project_id = $2)
		ORDER BY name`, orgID, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Secret{}
	for rows.Next() {
		var sec Secret
		if err := rows.Scan(&sec.ID, &sec.OrgID, &sec.ProjectID, &sec.Name, &sec.Ciphertext,
			&sec.Nonce, &sec.CreatedAt, &sec.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, sec)
	}
	return out, rows.Err()
}

func (s *Store) DeleteSecret(ctx context.Context, orgID, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM secrets WHERE org_id = $1 AND id = $2`, orgID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SecretsForProject returns org-level and project-level secrets, project values winning.
func (s *Store) SecretsForProject(ctx context.Context, orgID, projectID uuid.UUID) ([]Secret, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, org_id, project_id, name, ciphertext, nonce, created_at, updated_at
		FROM secrets
		WHERE org_id = $1 AND (project_id IS NULL OR project_id = $2)
		ORDER BY (project_id IS NULL) DESC, name`, orgID, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Secret{}
	for rows.Next() {
		var sec Secret
		if err := rows.Scan(&sec.ID, &sec.OrgID, &sec.ProjectID, &sec.Name, &sec.Ciphertext,
			&sec.Nonce, &sec.CreatedAt, &sec.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, sec)
	}
	return out, rows.Err()
}

// ProjectWorkflowRunSHA is used to detect duplicate triggers.
func (s *Store) RunExistsForSHA(ctx context.Context, workflowID uuid.UUID, sha, event string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM workflow_runs WHERE workflow_id = $1 AND sha = $2 AND event = $3)`,
		workflowID, sha, event).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return exists, err
}
