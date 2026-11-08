package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Store) CreateProject(ctx context.Context, orgID uuid.UUID, slug, name, repoURL, repoFullName, localPath, branch string) (Project, error) {
	if branch == "" {
		branch = "main"
	}
	var p Project
	err := s.pool.QueryRow(ctx, `
		INSERT INTO projects (org_id, slug, name, repo_url, repo_full_name, local_path, default_branch)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, org_id, slug, name, provider, repo_full_name, repo_url, default_branch,
		          local_path, created_at, updated_at`,
		orgID, slug, name, repoURL, repoFullName, localPath, branch).
		Scan(&p.ID, &p.OrgID, &p.Slug, &p.Name, &p.Provider, &p.RepoFullName, &p.RepoURL,
			&p.DefaultBranch, &p.LocalPath, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

const projectColumns = `id, org_id, slug, name, provider, repo_full_name, repo_url, default_branch,
	local_path, created_at, updated_at`

func scanProject(row pgx.Row) (Project, error) {
	var p Project
	err := row.Scan(&p.ID, &p.OrgID, &p.Slug, &p.Name, &p.Provider, &p.RepoFullName, &p.RepoURL,
		&p.DefaultBranch, &p.LocalPath, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	return p, err
}

func (s *Store) Projects(ctx context.Context, orgID uuid.UUID) ([]Project, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+projectColumns+` FROM projects WHERE org_id = $1 ORDER BY name`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ProjectsByRepoFullName finds projects bound to a provider repo across orgs.
// Webhook deliveries are instance-level, so the match is not org-scoped.
func (s *Store) ProjectsByRepoFullName(ctx context.Context, fullName string) ([]Project, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+projectColumns+` FROM projects WHERE LOWER(repo_full_name) = LOWER($1)`, fullName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ProjectOrg is the owning org of a project. Public run pages use it to
// read the org toggle without a session.
func (s *Store) ProjectOrg(ctx context.Context, projectID uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT org_id FROM projects WHERE id = $1`, projectID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	return id, err
}

func (s *Store) Project(ctx context.Context, orgID uuid.UUID, id string) (Project, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		// Accept the slug as an identifier as well; the UI links by slug.
		return scanProject(s.pool.QueryRow(ctx,
			`SELECT `+projectColumns+` FROM projects WHERE org_id = $1 AND slug = $2`, orgID, id))
	}
	return scanProject(s.pool.QueryRow(ctx,
		`SELECT `+projectColumns+` FROM projects WHERE org_id = $1 AND id = $2`, orgID, uid))
}

func (s *Store) UpsertWorkflow(ctx context.Context, projectID uuid.UUID, path, name string) (Workflow, error) {
	var w Workflow
	err := s.pool.QueryRow(ctx, `
		INSERT INTO workflows (project_id, path, name) VALUES ($1, $2, $3)
		ON CONFLICT (project_id, path) DO UPDATE SET name = EXCLUDED.name, updated_at = now()
		RETURNING id, project_id, path, name, created_at, updated_at`,
		projectID, path, name).
		Scan(&w.ID, &w.ProjectID, &w.Path, &w.Name, &w.CreatedAt, &w.UpdatedAt)
	return w, err
}

func (s *Store) Workflows(ctx context.Context, projectID uuid.UUID) ([]Workflow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, project_id, path, name, created_at, updated_at
		FROM workflows WHERE project_id = $1 ORDER BY path`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Workflow{}
	for rows.Next() {
		var w Workflow
		if err := rows.Scan(&w.ID, &w.ProjectID, &w.Path, &w.Name, &w.CreatedAt, &w.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *Store) WorkflowByID(ctx context.Context, id uuid.UUID) (Workflow, error) {
	var w Workflow
	err := s.pool.QueryRow(ctx, `
		SELECT id, project_id, path, name, created_at, updated_at FROM workflows WHERE id = $1`, id).
		Scan(&w.ID, &w.ProjectID, &w.Path, &w.Name, &w.CreatedAt, &w.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Workflow{}, ErrNotFound
	}
	return w, err
}

// PutWorkflowVersion stores an immutable version. Identical content for the same commit is reused.
func (s *Store) PutWorkflowVersion(ctx context.Context, workflowID uuid.UUID, sha, content string, parsed any, valid bool, diagnostics, deps any) (WorkflowVersion, error) {
	parsedJSON, err := json.Marshal(parsed)
	if err != nil {
		return WorkflowVersion{}, err
	}
	diagJSON, err := json.Marshal(diagnostics)
	if err != nil {
		return WorkflowVersion{}, err
	}
	depsJSON, err := json.Marshal(deps)
	if err != nil {
		return WorkflowVersion{}, err
	}
	var v WorkflowVersion
	err = s.pool.QueryRow(ctx, `
		INSERT INTO workflow_versions (workflow_id, commit_sha, content, parsed, valid, diagnostics, deps)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (workflow_id, commit_sha) DO UPDATE
		SET content = EXCLUDED.content, parsed = EXCLUDED.parsed,
		    valid = EXCLUDED.valid, diagnostics = EXCLUDED.diagnostics, deps = EXCLUDED.deps
		RETURNING id, workflow_id, commit_sha, content, parsed, valid, diagnostics, deps, created_at`,
		workflowID, sha, content, parsedJSON, valid, diagJSON, depsJSON).
		Scan(&v.ID, &v.WorkflowID, &v.CommitSHA, &v.Content, &v.Parsed, &v.Valid, &v.Diagnostics, &v.Deps, &v.CreatedAt)
	return v, err
}

func (s *Store) WorkflowVersion(ctx context.Context, id uuid.UUID) (WorkflowVersion, error) {
	var v WorkflowVersion
	err := s.pool.QueryRow(ctx, `
		SELECT id, workflow_id, commit_sha, content, parsed, valid, diagnostics, deps, created_at
		FROM workflow_versions WHERE id = $1`, id).
		Scan(&v.ID, &v.WorkflowID, &v.CommitSHA, &v.Content, &v.Parsed, &v.Valid, &v.Diagnostics, &v.Deps, &v.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return WorkflowVersion{}, ErrNotFound
	}
	return v, err
}

// LatestWorkflowVersion returns the most recent stored version for a workflow path.
func (s *Store) LatestWorkflowVersion(ctx context.Context, projectID uuid.UUID, path string) (Workflow, WorkflowVersion, error) {
	var w Workflow
	var v WorkflowVersion
	err := s.pool.QueryRow(ctx, `
		SELECT w.id, w.project_id, w.path, w.name, w.created_at, w.updated_at,
		       v.id, v.workflow_id, v.commit_sha, v.content, v.parsed, v.valid, v.diagnostics, v.created_at
		FROM workflows w
		JOIN workflow_versions v ON v.workflow_id = w.id
		WHERE w.project_id = $1 AND w.path = $2
		ORDER BY v.created_at DESC
		LIMIT 1`, projectID, path).
		Scan(&w.ID, &w.ProjectID, &w.Path, &w.Name, &w.CreatedAt, &w.UpdatedAt,
			&v.ID, &v.WorkflowID, &v.CommitSHA, &v.Content, &v.Parsed, &v.Valid, &v.Diagnostics, &v.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Workflow{}, WorkflowVersion{}, ErrNotFound
	}
	return w, v, err
}
