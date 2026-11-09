package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// EnrollToken is a one-time, expiring credential used by `kern-runner join` to
// register a new runner. The plaintext token is never stored.
type EnrollToken struct {
	ID        uuid.UUID       `json:"id"`
	OrgID     uuid.UUID       `json:"org_id"`
	Name      string          `json:"name"`
	Config    json.RawMessage `json:"config,omitempty"`
	ExpiresAt time.Time       `json:"expires_at"`
	UsedAt    *time.Time      `json:"used_at"`
	CreatedAt time.Time       `json:"created_at"`
}

// RunnerEnrollConfig is the desired runner configuration captured when an
// enroll token is minted; registerRunner applies it to the runner row.
type RunnerEnrollConfig struct {
	AutoUpdate *bool             `json:"auto_update,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
}

func (s *Store) CreateEnrollToken(ctx context.Context, orgID uuid.UUID, createdBy *uuid.UUID, name, tokenHash string, expires time.Time, config RunnerEnrollConfig) (EnrollToken, error) {
	raw, err := json.Marshal(config)
	if err != nil {
		return EnrollToken{}, err
	}
	var t EnrollToken
	err = s.pool.QueryRow(ctx, `
		INSERT INTO runner_enroll_tokens (org_id, created_by, name, token_hash, expires_at, config)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, org_id, name, config, expires_at, used_at, created_at`,
		orgID, createdBy, name, tokenHash, expires, raw).
		Scan(&t.ID, &t.OrgID, &t.Name, &t.Config, &t.ExpiresAt, &t.UsedAt, &t.CreatedAt)
	return t, err
}

// ConsumeEnrollToken atomically marks a token used and returns its org plus
// the desired runner config. It fails when the token is unknown, expired or
// already consumed — each token enrolls exactly one runner.
func (s *Store) ConsumeEnrollToken(ctx context.Context, tokenHash string) (uuid.UUID, RunnerEnrollConfig, error) {
	var orgID uuid.UUID
	var raw []byte
	err := s.pool.QueryRow(ctx, `
		UPDATE runner_enroll_tokens SET used_at = now()
		WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()
		RETURNING org_id, config`, tokenHash).Scan(&orgID, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, RunnerEnrollConfig{}, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, RunnerEnrollConfig{}, err
	}
	var cfg RunnerEnrollConfig
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &cfg)
	}
	return orgID, cfg, nil
}
