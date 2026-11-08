package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Var is a project configuration variable — the GitHub `vars` context.
// Unlike Secret it is stored and returned in plaintext: vars are
// configuration, not credentials.
type Var struct {
	ID        uuid.UUID `json:"id"`
	ProjectID uuid.UUID `json:"project_id"`
	Name      string    `json:"name"`
	Value     string    `json:"value"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const varCols = `id, project_id, name, value, created_at, updated_at`

func (s *Store) UpsertVar(ctx context.Context, projectID uuid.UUID, name, value string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO project_vars (project_id, name, value) VALUES ($1, $2, $3)
		ON CONFLICT (project_id, name)
		DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
		projectID, name, value)
	return err
}

// VarsForProject returns every var of a project ordered by name.
func (s *Store) VarsForProject(ctx context.Context, projectID uuid.UUID) ([]Var, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+varCols+` FROM project_vars WHERE project_id = $1 ORDER BY name`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanVars(rows)
}

func (s *Store) DeleteVar(ctx context.Context, projectID uuid.UUID, name string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM project_vars WHERE project_id = $1 AND name = $2`, projectID, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func scanVars(rows pgx.Rows) ([]Var, error) {
	out := []Var{}
	for rows.Next() {
		var v Var
		if err := rows.Scan(&v.ID, &v.ProjectID, &v.Name, &v.Value, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
