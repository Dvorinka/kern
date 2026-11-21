package store

import (
	"context"
	"strings"

	"github.com/google/uuid"
)

// ParseIDPrefix parses a full UUID or a 4-32 hex-char prefix (hyphens allowed).
// Returns (exactID, "", true) for a full UUID, (uuid.Nil, hexPrefix, true) for
// a valid prefix, or (uuid.Nil, "", false) for invalid input.
func ParseIDPrefix(raw string) (uuid.UUID, string, bool) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if id, err := uuid.Parse(raw); err == nil {
		return id, "", true
	}
	clean := strings.ReplaceAll(raw, "-", "")
	if len(clean) < 4 || len(clean) > 32 {
		return uuid.Nil, "", false
	}
	for _, r := range clean {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return uuid.Nil, "", false
		}
	}
	return uuid.Nil, clean, true
}

func (s *Store) scanUUIDs(ctx context.Context, query string, args ...any) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// JobPrefix resolves up to 8 job IDs in orgID matching a hex UUID prefix.
func (s *Store) JobPrefix(ctx context.Context, orgID uuid.UUID, prefix string) ([]uuid.UUID, error) {
	return s.scanUUIDs(ctx, `
		SELECT j.id FROM jobs j
		JOIN workflow_runs r ON r.id = j.run_id
		JOIN projects p ON p.id = r.project_id
		WHERE p.org_id = $1 AND replace(j.id::text, '-', '') LIKE $2 || '%'
		ORDER BY j.created_at DESC LIMIT 8`, orgID, prefix)
}

// ArtifactPrefix resolves up to 8 artifact IDs in orgID matching a hex UUID prefix.
func (s *Store) ArtifactPrefix(ctx context.Context, orgID uuid.UUID, prefix string) ([]uuid.UUID, error) {
	return s.scanUUIDs(ctx, `
		SELECT a.id FROM artifacts a
		JOIN workflow_runs r ON r.id = a.run_id
		JOIN projects p ON p.id = r.project_id
		WHERE p.org_id = $1 AND replace(a.id::text, '-', '') LIKE $2 || '%'
		ORDER BY a.created_at DESC LIMIT 8`, orgID, prefix)
}

// CachePrefix resolves up to 8 cache entry IDs in orgID matching a hex UUID prefix.
func (s *Store) CachePrefix(ctx context.Context, orgID uuid.UUID, prefix string) ([]uuid.UUID, error) {
	return s.scanUUIDs(ctx, `
		SELECT c.id FROM cache_entries c
		JOIN projects p ON p.id = c.project_id
		WHERE p.org_id = $1 AND replace(c.id::text, '-', '') LIKE $2 || '%'
		ORDER BY c.created_at DESC LIMIT 8`, orgID, prefix)
}
