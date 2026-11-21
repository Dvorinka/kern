package store

// DB-backed state for the GHA-protocol chunked cache upload (#369): the
// in-process ghUpload map pinned reserve→commit to one API replica and to
// one machine's temp dir. Sessions and chunk inventory live here; chunk
// bytes live in blob storage as immutable per-offset objects.

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type GHUpload struct {
	ID      int64
	JobID   uuid.UUID
	Key     string
	Version string
	Scope   string
	BlobRef string
	Created time.Time
}

type GHChunk struct {
	Start   int64
	Stop    int64
	BlobRef string
}

func (s *Store) CreateGHUpload(ctx context.Context, jobID uuid.UUID, key, version, scope, blobRef string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO gh_cache_uploads (job_id, key, version, scope, blob_ref)
		 VALUES ($1,$2,$3,$4,$5) RETURNING id`,
		jobID, key, version, scope, blobRef).Scan(&id)
	return id, err
}

func (s *Store) GHUpload(ctx context.Context, id int64) (GHUpload, error) {
	var u GHUpload
	err := s.pool.QueryRow(ctx,
		`SELECT id, job_id, key, version, scope, blob_ref, created_at
		 FROM gh_cache_uploads WHERE id=$1`, id).
		Scan(&u.ID, &u.JobID, &u.Key, &u.Version, &u.Scope, &u.BlobRef, &u.Created)
	if err != nil {
		return GHUpload{}, err
	}
	return u, nil
}

// DeleteGHUpload claims the reservation for commit: exactly one caller gets
// the row back, so concurrent commits on any replica are safe.
func (s *Store) DeleteGHUpload(ctx context.Context, id int64) (GHUpload, bool, error) {
	var u GHUpload
	err := s.pool.QueryRow(ctx,
		`DELETE FROM gh_cache_uploads WHERE id=$1
		 RETURNING id, job_id, key, version, scope, blob_ref, created_at`, id).
		Scan(&u.ID, &u.JobID, &u.Key, &u.Version, &u.Scope, &u.BlobRef, &u.Created)
	if errors.Is(err, pgx.ErrNoRows) {
		return GHUpload{}, false, nil
	}
	if err != nil {
		return GHUpload{}, false, err
	}
	return u, true, nil
}

// InsertGHChunk records one uploaded range. A re-sent chunk at the same
// offset updates the row (its blob object is idempotently overwritten).
func (s *Store) InsertGHChunk(ctx context.Context, uploadID, start, stop int64, blobRef string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO gh_cache_upload_chunks (upload_id, start, stop, blob_ref)
		 VALUES ($1,$2,$3,$4)
		 ON CONFLICT (upload_id, start) DO UPDATE SET stop=$3, blob_ref=$4`,
		uploadID, start, stop, blobRef)
	return err
}

func (s *Store) GHChunks(ctx context.Context, uploadID int64) ([]GHChunk, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT start, stop, blob_ref FROM gh_cache_upload_chunks
		 WHERE upload_id=$1 ORDER BY start`, uploadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GHChunk
	for rows.Next() {
		var ch GHChunk
		if err := rows.Scan(&ch.Start, &ch.Stop, &ch.BlobRef); err != nil {
			return nil, err
		}
		out = append(out, ch)
	}
	return out, rows.Err()
}

// StaleGHUploads lists reservations older than cutoff for the sweeper.
func (s *Store) StaleGHUploads(ctx context.Context, cutoff time.Time) ([]GHUpload, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, job_id, key, version, scope, blob_ref, created_at
		 FROM gh_cache_uploads WHERE created_at < $1`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GHUpload
	for rows.Next() {
		var u GHUpload
		if err := rows.Scan(&u.ID, &u.JobID, &u.Key, &u.Version, &u.Scope, &u.BlobRef, &u.Created); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
