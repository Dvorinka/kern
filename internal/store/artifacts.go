package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Artifact struct {
	ID        uuid.UUID  `json:"id"`
	RunID     uuid.UUID  `json:"run_id"`
	JobID     *uuid.UUID `json:"job_id"`
	Name      string     `json:"name"`
	SizeBytes int64      `json:"size_bytes"`
	SHA256    string     `json:"sha256"`
	BlobRef   string     `json:"-"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at"`
	// Joined fields for listings.
	RunNumber   int    `json:"run_number"`
	ProjectSlug string `json:"project_slug"`
}

func (s *Store) CreateArtifact(ctx context.Context, runID, jobID uuid.UUID, name, blobRef string, size int64, sha256 string) (Artifact, error) {
	var a Artifact
	err := s.pool.QueryRow(ctx, `
		INSERT INTO artifacts (run_id, job_id, name, blob_ref, size_bytes, sha256, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, now() + interval '90 days')
		ON CONFLICT (run_id, name) DO UPDATE SET
			job_id = EXCLUDED.job_id, blob_ref = EXCLUDED.blob_ref,
			size_bytes = EXCLUDED.size_bytes, sha256 = EXCLUDED.sha256
		RETURNING id, run_id, job_id, name, size_bytes, sha256, blob_ref, created_at, expires_at`,
		runID, jobID, name, blobRef, size, sha256).
		Scan(&a.ID, &a.RunID, &a.JobID, &a.Name, &a.SizeBytes, &a.SHA256, &a.BlobRef, &a.CreatedAt, &a.ExpiresAt)
	return a, err
}

func (s *Store) ArtifactByName(ctx context.Context, runID uuid.UUID, name string) (Artifact, error) {
	var a Artifact
	err := s.pool.QueryRow(ctx, `
		SELECT id, run_id, job_id, name, size_bytes, sha256, blob_ref, created_at, expires_at
		FROM artifacts WHERE run_id = $1 AND name = $2`, runID, name).
		Scan(&a.ID, &a.RunID, &a.JobID, &a.Name, &a.SizeBytes, &a.SHA256, &a.BlobRef, &a.CreatedAt, &a.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Artifact{}, ErrNotFound
	}
	return a, err
}

func (s *Store) Artifact(ctx context.Context, id uuid.UUID) (Artifact, error) {
	var a Artifact
	err := s.pool.QueryRow(ctx, `
		SELECT id, run_id, job_id, name, size_bytes, sha256, blob_ref, created_at, expires_at
		FROM artifacts WHERE id = $1`, id).
		Scan(&a.ID, &a.RunID, &a.JobID, &a.Name, &a.SizeBytes, &a.SHA256, &a.BlobRef, &a.CreatedAt, &a.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Artifact{}, ErrNotFound
	}
	return a, err
}

// DeleteExpiredArtifacts removes rows past expires_at and returns their blob
// references so the caller can drop the archives. Rows with NULL expires_at
// never expire.
func (s *Store) DeleteExpiredArtifacts(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		DELETE FROM artifacts WHERE expires_at IS NOT NULL AND expires_at < now()
		RETURNING blob_ref`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var refs []string
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

// DeleteArtifact removes one artifact owned by the org (via run→project join)
// and returns its blob reference.
func (s *Store) DeleteArtifact(ctx context.Context, orgID, id uuid.UUID) (string, error) {
	var ref string
	err := s.pool.QueryRow(ctx, `
		DELETE FROM artifacts a USING workflow_runs r, projects p
		WHERE a.id = $1 AND r.id = a.run_id AND p.id = r.project_id AND p.org_id = $2
		RETURNING a.blob_ref`, id, orgID).Scan(&ref)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return ref, err
}

// ArtifactBytesForProject totals live artifact bytes for quota checks.
func (s *Store) ArtifactBytesForProject(ctx context.Context, projectID uuid.UUID) (int64, error) {
	var total int64
	err := s.pool.QueryRow(ctx, `
		SELECT coalesce(sum(a.size_bytes), 0)
		FROM artifacts a
		JOIN workflow_runs r ON r.id = a.run_id
		WHERE r.project_id = $1`, projectID).Scan(&total)
	return total, err
}

func (s *Store) Artifacts(ctx context.Context, orgID uuid.UUID, runID *uuid.UUID, limit int) ([]Artifact, error) {
	limit, _ = page(limit, 0)
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.run_id, a.job_id, a.name, a.size_bytes, a.sha256, a.blob_ref,
		       a.created_at, a.expires_at, r.run_number, p.slug
		FROM artifacts a
		JOIN workflow_runs r ON r.id = a.run_id
		JOIN projects p ON p.id = r.project_id
		WHERE p.org_id = $1 AND ($2::uuid IS NULL OR a.run_id = $2)
		ORDER BY a.created_at DESC LIMIT $3`, orgID, runID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Artifact{}
	for rows.Next() {
		var a Artifact
		if err := rows.Scan(&a.ID, &a.RunID, &a.JobID, &a.Name, &a.SizeBytes, &a.SHA256,
			&a.BlobRef, &a.CreatedAt, &a.ExpiresAt, &a.RunNumber, &a.ProjectSlug); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
