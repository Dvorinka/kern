package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrExists is returned when a uniqueness guard prevents an insert.
var ErrExists = errors.New("already exists")

// CacheEntry is one saved cache archive. GitHub scopes caches per repository and
// branch; KERN approximates that with (project_id, scope) where scope is derived
// from the run's trust level.
type CacheEntry struct {
	ID         uuid.UUID  `json:"id"`
	ProjectID  uuid.UUID  `json:"project_id"`
	Key        string     `json:"key"`
	Version    string     `json:"version"`
	Scope      string     `json:"scope"`
	SizeBytes  int64      `json:"size_bytes"`
	BlobRef    string     `json:"-"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt time.Time  `json:"last_used_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
}

const cacheCols = `id, project_id, key, version, scope, size_bytes, blob_ref,
	created_at, last_used_at, expires_at`

func scanCacheEntry(row pgx.Row) (CacheEntry, error) {
	var e CacheEntry
	err := row.Scan(&e.ID, &e.ProjectID, &e.Key, &e.Version, &e.Scope,
		&e.SizeBytes, &e.BlobRef, &e.CreatedAt, &e.LastUsedAt, &e.ExpiresAt)
	return e, err
}

// CacheLookup resolves a cache key the way GitHub does: an exact match wins;
// otherwise each restore prefix is tried in order against the newest entry.
func (s *Store) CacheLookup(ctx context.Context, projectID uuid.UUID, key string, prefixes, scopes []string) (CacheEntry, error) {
	e, err := scanCacheEntry(s.pool.QueryRow(ctx,
		`SELECT `+cacheCols+` FROM cache_entries
		 WHERE project_id = $1 AND key = $2 AND scope = ANY($3)
		 ORDER BY created_at DESC LIMIT 1`, projectID, key, scopes))
	if err == nil {
		return e, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return CacheEntry{}, err
	}
	for _, p := range prefixes {
		if strings.TrimSpace(p) == "" {
			continue
		}
		e, err = scanCacheEntry(s.pool.QueryRow(ctx,
			`SELECT `+cacheCols+` FROM cache_entries
			 WHERE project_id = $1 AND starts_with(key, $2) AND scope = ANY($3)
			 ORDER BY created_at DESC LIMIT 1`, projectID, p, scopes))
		if err == nil {
			return e, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return CacheEntry{}, err
		}
	}
	return CacheEntry{}, ErrNotFound
}

// SaveCacheEntry records a saved archive. Caches are immutable: an existing
// (project, key, version, scope) row yields ErrExists, matching GitHub's
// "another job may be creating this cache" behaviour.
func (s *Store) SaveCacheEntry(ctx context.Context, projectID uuid.UUID, key, version, scope, blobRef string, size int64) (CacheEntry, error) {
	e, err := scanCacheEntry(s.pool.QueryRow(ctx, `
		INSERT INTO cache_entries (project_id, key, version, scope, blob_ref, size_bytes)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (project_id, key, version, scope) DO NOTHING
		RETURNING `+cacheCols, projectID, key, version, scope, blobRef, size))
	if errors.Is(err, pgx.ErrNoRows) {
		return CacheEntry{}, ErrExists
	}
	return e, err
}

// DeleteStaleCacheEntries evicts entries GitHub would consider dead: untouched
// for maxAge (GitHub evicts after 7 days without a hit) or past an explicit
// expires_at. Returns blob references so the caller can drop the archives.
func (s *Store) DeleteStaleCacheEntries(ctx context.Context, maxAge time.Duration) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		DELETE FROM cache_entries
		WHERE last_used_at < now() - make_interval(secs => $1)
		   OR (expires_at IS NOT NULL AND expires_at < now())
		RETURNING blob_ref`, int64(maxAge.Seconds()))
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

// TouchCache refreshes last_used_at for eviction bookkeeping.
func (s *Store) TouchCache(ctx context.Context, id uuid.UUID) {
	_, _ = s.pool.Exec(ctx, `UPDATE cache_entries SET last_used_at = now() WHERE id = $1`, id)
}

// DeleteCacheEntry removes an entry owned by the org and returns its blob
// reference so the caller can drop the archive as well.
func (s *Store) DeleteCacheEntry(ctx context.Context, orgID, id uuid.UUID) (string, error) {
	var blobRef string
	err := s.pool.QueryRow(ctx, `
		DELETE FROM cache_entries c USING projects p
		WHERE c.id = $1 AND p.id = c.project_id AND p.org_id = $2
		RETURNING c.blob_ref`, id, orgID).Scan(&blobRef)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return blobRef, err
}

// CacheEntries lists entries for the org overview/metrics surfaces.
func (s *Store) CacheEntries(ctx context.Context, orgID uuid.UUID, limit int) ([]CacheEntry, error) {
	limit, _ = page(limit, 0)
	rows, err := s.pool.Query(ctx, `
		SELECT c.id, c.project_id, c.key, c.version, c.scope, c.size_bytes, c.blob_ref,
		       c.created_at, c.last_used_at, c.expires_at
		FROM cache_entries c
		JOIN projects p ON p.id = c.project_id
		WHERE p.org_id = $1
		ORDER BY c.last_used_at DESC LIMIT $2`, orgID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CacheEntry{}
	for rows.Next() {
		e, err := scanCacheEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
