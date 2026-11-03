package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// EnrollToken is a one-time, expiring credential used by `kern-runner join` to
// register a new runner. The plaintext token is never stored.
type EnrollToken struct {
	ID        uuid.UUID  `json:"id"`
	OrgID     uuid.UUID  `json:"org_id"`
	Name      string     `json:"name"`
	ExpiresAt time.Time  `json:"expires_at"`
	UsedAt    *time.Time `json:"used_at"`
	CreatedAt time.Time  `json:"created_at"`
}

func (s *Store) CreateEnrollToken(ctx context.Context, orgID uuid.UUID, createdBy *uuid.UUID, name, tokenHash string, expires time.Time) (EnrollToken, error) {
	var t EnrollToken
	err := s.pool.QueryRow(ctx, `
		INSERT INTO runner_enroll_tokens (org_id, created_by, name, token_hash, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, org_id, name, expires_at, used_at, created_at`,
		orgID, createdBy, name, tokenHash, expires).
		Scan(&t.ID, &t.OrgID, &t.Name, &t.ExpiresAt, &t.UsedAt, &t.CreatedAt)
	return t, err
}

// ConsumeEnrollToken atomically marks a token used and returns its org. It
// fails when the token is unknown, expired or already consumed — each token
// enrolls exactly one runner.
func (s *Store) ConsumeEnrollToken(ctx context.Context, tokenHash string) (uuid.UUID, error) {
	var orgID uuid.UUID
	err := s.pool.QueryRow(ctx, `
		UPDATE runner_enroll_tokens SET used_at = now()
		WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()
		RETURNING org_id`, tokenHash).Scan(&orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	return orgID, err
}
