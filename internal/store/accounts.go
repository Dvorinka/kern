package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrNotFound = errors.New("not found")

// ErrConflict marks a state guard violation (e.g. demoting the last owner).
var ErrConflict = errors.New("conflict")

// Bootstrap creates the default organization and admin user when they do not exist.
func (s *Store) Bootstrap(ctx context.Context, orgSlug, orgName, email, name, passwordHash string) (User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback(ctx)

	var orgID uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO orgs (slug, name) VALUES ($1, $2)
		ON CONFLICT (slug) DO UPDATE SET name = orgs.name
		RETURNING id`, orgSlug, orgName).Scan(&orgID)
	if err != nil {
		return User{}, fmt.Errorf("bootstrap org: %w", err)
	}

	var userID uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO users (email, name, password_hash) VALUES ($1, $2, $3)
		ON CONFLICT (email) DO UPDATE SET name = users.name
		RETURNING id`, email, name, passwordHash).Scan(&userID)
	if err != nil {
		return User{}, fmt.Errorf("bootstrap user: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO org_members (org_id, user_id, role) VALUES ($1, $2, 'owner')
		ON CONFLICT DO NOTHING`, orgID, userID); err != nil {
		return User{}, fmt.Errorf("bootstrap membership: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, err
	}
	return s.UserByEmail(ctx, email)
}

func (s *Store) UserByEmail(ctx context.Context, email string) (User, error) {
	var u User
	err := s.pool.QueryRow(ctx, `
		SELECT u.id, u.email, u.name, u.password_hash, u.created_at,
		       COALESCE(m.org_id, '00000000-0000-0000-0000-000000000000'::uuid),
		       COALESCE(m.role, 'member')
		FROM users u
		LEFT JOIN org_members m ON m.user_id = u.id
		WHERE u.email = $1
		ORDER BY m.created_at
		LIMIT 1`, email).
		Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.CreatedAt, &u.OrgID, &u.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

// OrgUserIDsByEmail resolves org members by email. Missing emails are an error.
// Returned ids are lowercase uuid strings, matching environments.reviewers.
func (s *Store) OrgUserIDsByEmail(ctx context.Context, orgID uuid.UUID, emails []string) ([]string, error) {
	if len(emails) == 0 {
		return []string{}, nil
	}
	want := make([]string, len(emails))
	for i, e := range emails {
		want[i] = strings.ToLower(strings.TrimSpace(e))
	}
	rows, err := s.pool.Query(ctx, `
		SELECT lower(u.email), u.id::text
		FROM users u
		JOIN org_members m ON m.user_id = u.id
		WHERE m.org_id = $1 AND lower(u.email) = ANY($2)`, orgID, want)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := map[string]string{}
	for rows.Next() {
		var email, id string
		if err := rows.Scan(&email, &id); err != nil {
			return nil, err
		}
		found[email] = id
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(want))
	var missing []string
	seen := map[string]bool{}
	for _, email := range want {
		if seen[email] {
			continue
		}
		seen[email] = true
		id, ok := found[email]
		if !ok {
			missing = append(missing, email)
			continue
		}
		ids = append(ids, id)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("unknown org member: %s", strings.Join(missing, ", "))
	}
	return ids, nil
}

func (s *Store) UserByID(ctx context.Context, id uuid.UUID) (User, error) {
	var u User
	err := s.pool.QueryRow(ctx, `
		SELECT u.id, u.email, u.name, u.password_hash, u.created_at,
		       COALESCE(m.org_id, '00000000-0000-0000-0000-000000000000'::uuid),
		       COALESCE(m.role, 'member')
		FROM users u
		LEFT JOIN org_members m ON m.user_id = u.id
		WHERE u.id = $1
		ORDER BY m.created_at
		LIMIT 1`, id).
		Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.CreatedAt, &u.OrgID, &u.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

type Member struct {
	ID        uuid.UUID `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Store) Members(ctx context.Context, orgID uuid.UUID) ([]Member, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT u.id, u.email, u.name, m.role, m.created_at
		FROM org_members m JOIN users u ON u.id = m.user_id
		WHERE m.org_id = $1 ORDER BY m.created_at`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Member{}
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.ID, &m.Email, &m.Name, &m.Role, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// CreateMember inserts a user and grants them an org membership in one
// transaction. A duplicate email returns ErrExists-flavored text for the API
// to map to 409.
func (s *Store) CreateMember(ctx context.Context, orgID uuid.UUID, email, name, passwordHash, role string) (User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback(ctx)
	var userID uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO users (email, name, password_hash) VALUES ($1, $2, $3)
		RETURNING id`, email, name, passwordHash).Scan(&userID)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "23505") {
			return User{}, fmt.Errorf("%w: email already registered", ErrExists)
		}
		return User{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO org_members (org_id, user_id, role) VALUES ($1, $2, $3)`,
		orgID, userID, role); err != nil {
		return User{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, err
	}
	return s.UserByEmail(ctx, email)
}

// SetMemberRole updates a member's role. The last owner cannot be demoted —
// checked inside the transaction so a concurrent demotion cannot race it.
func (s *Store) SetMemberRole(ctx context.Context, orgID, userID uuid.UUID, role string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var current string
	err = tx.QueryRow(ctx, `
		SELECT role FROM org_members WHERE org_id = $1 AND user_id = $2 FOR UPDATE`,
		orgID, userID).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if current == "owner" && role != "owner" {
		var owners int
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*) FROM org_members WHERE org_id = $1 AND role = 'owner'`,
			orgID).Scan(&owners); err != nil {
			return err
		}
		if owners <= 1 {
			return fmt.Errorf("%w: cannot demote the last owner", ErrConflict)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE org_members SET role = $3 WHERE org_id = $1 AND user_id = $2`,
		orgID, userID, role); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RemoveMember deletes a membership plus the user's sessions and API tokens,
// so a removed member cannot authenticate at all. The last owner cannot be
// removed.
func (s *Store) RemoveMember(ctx context.Context, orgID, userID uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var current string
	err = tx.QueryRow(ctx, `
		SELECT role FROM org_members WHERE org_id = $1 AND user_id = $2 FOR UPDATE`,
		orgID, userID).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if current == "owner" {
		var owners int
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*) FROM org_members WHERE org_id = $1 AND role = 'owner'`,
			orgID).Scan(&owners); err != nil {
			return err
		}
		if owners <= 1 {
			return fmt.Errorf("%w: cannot remove the last owner", ErrConflict)
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM org_members WHERE org_id = $1 AND user_id = $2`, orgID, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM api_tokens WHERE user_id = $1`, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) CreateSession(ctx context.Context, userID uuid.UUID, tokenHash string, expiresAt time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO sessions (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
		userID, tokenHash, expiresAt)
	return err
}

func (s *Store) UserBySession(ctx context.Context, tokenHash string) (User, error) {
	var u User
	err := s.pool.QueryRow(ctx, `
		SELECT u.id, u.email, u.name, u.password_hash, u.created_at,
		       COALESCE(m.org_id, '00000000-0000-0000-0000-000000000000'::uuid),
		       COALESCE(m.role, 'member')
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		LEFT JOIN org_members m ON m.user_id = u.id
		WHERE s.token_hash = $1 AND s.expires_at > now()
		ORDER BY m.created_at
		LIMIT 1`, tokenHash).
		Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.CreatedAt, &u.OrgID, &u.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash)
	return err
}

// SessionInfo is one active login session. TokenHash is internal — it lets the
// API mark the caller's current session without ever exposing the hash.
type SessionInfo struct {
	ID        uuid.UUID `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Current   bool      `json:"current"`
	TokenHash string    `json:"-"`
}

// ListSessions returns a user's unexpired sessions, newest first.
func (s *Store) ListSessions(ctx context.Context, userID uuid.UUID) ([]SessionInfo, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, token_hash, created_at, expires_at
		FROM sessions WHERE user_id = $1 AND expires_at > now()
		ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SessionInfo{}
	for rows.Next() {
		var se SessionInfo
		if err := rows.Scan(&se.ID, &se.TokenHash, &se.CreatedAt, &se.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, se)
	}
	return out, rows.Err()
}

// DeleteSessionByID removes one of the user's own sessions.
func (s *Store) DeleteSessionByID(ctx context.Context, userID, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1 AND id = $2`, userID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteOtherSessions removes every session of the user except keepHash —
// the "sign out everywhere else" path. Returns the number revoked.
func (s *Store) DeleteOtherSessions(ctx context.Context, userID uuid.UUID, keepHash string) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM sessions WHERE user_id = $1 AND token_hash <> $2`, userID, keepHash)
	return tag.RowsAffected(), err
}

type APIToken struct {
	ID         uuid.UUID  `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Scopes     []string   `json:"scopes"`
	LastUsedAt *time.Time `json:"last_used_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

func (s *Store) CreateAPIToken(ctx context.Context, userID uuid.UUID, name, prefix, hash string, scopes []string, expiresAt *time.Time) (APIToken, error) {
	var t APIToken
	err := s.pool.QueryRow(ctx, `
		INSERT INTO api_tokens (user_id, name, prefix, token_hash, scopes, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, name, prefix, scopes, last_used_at, expires_at, created_at`,
		userID, name, prefix, hash, scopes, expiresAt).
		Scan(&t.ID, &t.Name, &t.Prefix, &t.Scopes, &t.LastUsedAt, &t.ExpiresAt, &t.CreatedAt)
	return t, err
}

func (s *Store) ListAPITokens(ctx context.Context, userID uuid.UUID) ([]APIToken, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, prefix, scopes, last_used_at, expires_at, created_at
		FROM api_tokens WHERE user_id = $1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIToken{}
	for rows.Next() {
		var t APIToken
		if err := rows.Scan(&t.ID, &t.Name, &t.Prefix, &t.Scopes, &t.LastUsedAt, &t.ExpiresAt, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) DeleteAPIToken(ctx context.Context, userID, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM api_tokens WHERE user_id = $1 AND id = $2`, userID, id)
	return err
}

func (s *Store) UserByAPIToken(ctx context.Context, hash string) (User, []string, error) {
	var u User
	var scopes []string
	err := s.pool.QueryRow(ctx, `
		SELECT u.id, u.email, u.name, u.password_hash, u.created_at,
		       COALESCE(m.org_id, '00000000-0000-0000-0000-000000000000'::uuid),
		       COALESCE(m.role, 'member'), t.scopes
		FROM api_tokens t
		JOIN users u ON u.id = t.user_id
		LEFT JOIN org_members m ON m.user_id = u.id
		WHERE t.token_hash = $1 AND (t.expires_at IS NULL OR t.expires_at > now())
		ORDER BY m.created_at
		LIMIT 1`, hash).
		Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.CreatedAt, &u.OrgID, &u.Role, &scopes)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, nil, ErrNotFound
	}
	if err != nil {
		return User{}, nil, err
	}
	_, _ = s.pool.Exec(ctx, `UPDATE api_tokens SET last_used_at = now() WHERE token_hash = $1`, hash)
	return u, scopes, nil
}

func (s *Store) Audit(ctx context.Context, orgID uuid.UUID, actor *uuid.UUID, action, target string) {
	s.AuditMeta(ctx, orgID, actor, action, target, nil)
}

// AuditMeta is Audit with a metadata payload for actions where the target
// string alone is not the record (e.g. the approved fix commands).
func (s *Store) AuditMeta(ctx context.Context, orgID uuid.UUID, actor *uuid.UUID, action, target string, meta map[string]any) {
	m, _ := json.Marshal(meta)
	_, _ = s.pool.Exec(ctx, `
		INSERT INTO audit_events (org_id, actor_user_id, action, target, metadata) VALUES ($1, $2, $3, $4, $5)`,
		orgID, actor, action, target, m)
}
