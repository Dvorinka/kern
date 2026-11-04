package store

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const secretCols = `id, org_id, project_id, environment_id, name, ciphertext, nonce, created_at, updated_at`

func (s *Store) UpsertSecret(ctx context.Context, orgID uuid.UUID, projectID, envID *uuid.UUID, name string, ciphertext, nonce []byte) error {
	switch {
	case envID != nil:
		_, err := s.pool.Exec(ctx, `
			INSERT INTO secrets (org_id, project_id, environment_id, name, ciphertext, nonce)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (environment_id, name) WHERE environment_id IS NOT NULL
			DO UPDATE SET ciphertext = EXCLUDED.ciphertext, nonce = EXCLUDED.nonce, updated_at = now()`,
			orgID, projectID, *envID, name, ciphertext, nonce)
		return err
	case projectID == nil:
		_, err := s.pool.Exec(ctx, `
			INSERT INTO secrets (org_id, name, ciphertext, nonce) VALUES ($1, $2, $3, $4)
			ON CONFLICT (org_id, name) WHERE project_id IS NULL AND environment_id IS NULL
			DO UPDATE SET ciphertext = EXCLUDED.ciphertext, nonce = EXCLUDED.nonce, updated_at = now()`,
			orgID, name, ciphertext, nonce)
		return err
	default:
		_, err := s.pool.Exec(ctx, `
			INSERT INTO secrets (org_id, project_id, name, ciphertext, nonce) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (project_id, name) WHERE project_id IS NOT NULL AND environment_id IS NULL
			DO UPDATE SET ciphertext = EXCLUDED.ciphertext, nonce = EXCLUDED.nonce, updated_at = now()`,
			orgID, *projectID, name, ciphertext, nonce)
		return err
	}
}

func (s *Store) Secrets(ctx context.Context, orgID uuid.UUID, projectID, envID *uuid.UUID) ([]Secret, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+secretCols+`
		FROM secrets
		WHERE org_id = $1 AND ($2::uuid IS NULL OR project_id = $2)
		  AND (($3::uuid IS NULL AND environment_id IS NULL) OR environment_id = $3)
		ORDER BY name`, orgID, projectID, envID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSecrets(rows)
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
// Environment-scoped secrets are never included; they resolve per job.
func (s *Store) SecretsForProject(ctx context.Context, orgID, projectID uuid.UUID) ([]Secret, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+secretCols+`
		FROM secrets
		WHERE org_id = $1 AND (project_id IS NULL OR project_id = $2) AND environment_id IS NULL
		ORDER BY (project_id IS NULL) DESC, name`, orgID, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSecrets(rows)
}

// SecretsForEnvironment returns the secrets bound to a project's named
// environment (nothing when the environment does not exist).
func (s *Store) SecretsForEnvironment(ctx context.Context, projectID uuid.UUID, envName string) ([]Secret, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+secretCols+`
		FROM secrets sec JOIN environments e ON e.id = sec.environment_id
		WHERE e.project_id = $1 AND e.name = $2
		ORDER BY sec.name`, projectID, envName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSecrets(rows)
}

func scanSecrets(rows pgx.Rows) ([]Secret, error) {
	out := []Secret{}
	for rows.Next() {
		var sec Secret
		if err := rows.Scan(&sec.ID, &sec.OrgID, &sec.ProjectID, &sec.EnvironmentID, &sec.Name,
			&sec.Ciphertext, &sec.Nonce, &sec.CreatedAt, &sec.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, sec)
	}
	return out, rows.Err()
}
