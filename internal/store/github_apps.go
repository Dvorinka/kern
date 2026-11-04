package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// GitHubApp is an org-owned GitHub App created via the manifest flow. pem and
// webhook secret arrive as ciphertext from secrets.Cipher — the store never
// sees plaintext.
type GitHubApp struct {
	ID               uuid.UUID `json:"id"`
	OrgID            uuid.UUID `json:"org_id"`
	AppID            int64     `json:"app_id"`
	Slug             string    `json:"slug"`
	Name             string    `json:"name"`
	PEMCiphertext    []byte    `json:"-"`
	PEMNonce         []byte    `json:"-"`
	SecretCiphertext []byte    `json:"-"`
	SecretNonce      []byte    `json:"-"`
	HTMLURL          string    `json:"html_url"`
	CreatedBy        uuid.UUID `json:"created_by"`
	CreatedAt        time.Time `json:"created_at"`
}

const ghAppCols = `id, org_id, app_id, slug, name, pem_ciphertext, pem_nonce,
	secret_ciphertext, secret_nonce, html_url, created_by, created_at`

func scanGitHubApp(row interface{ Scan(...any) error }) (GitHubApp, error) {
	var a GitHubApp
	var createdBy *uuid.UUID
	err := row.Scan(&a.ID, &a.OrgID, &a.AppID, &a.Slug, &a.Name,
		&a.PEMCiphertext, &a.PEMNonce, &a.SecretCiphertext, &a.SecretNonce,
		&a.HTMLURL, &createdBy, &a.CreatedAt)
	if createdBy != nil {
		a.CreatedBy = *createdBy
	}
	return a, err
}

// PutGitHubApp stores a converted manifest app; re-registering the same app_id
// for an org refreshes the credentials.
func (s *Store) PutGitHubApp(ctx context.Context, a GitHubApp) (GitHubApp, error) {
	return scanGitHubApp(s.pool.QueryRow(ctx, `
		INSERT INTO github_apps (org_id, app_id, slug, name,
			pem_ciphertext, pem_nonce, secret_ciphertext, secret_nonce,
			html_url, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (org_id, app_id) DO UPDATE SET
			slug = EXCLUDED.slug, name = EXCLUDED.name,
			pem_ciphertext = EXCLUDED.pem_ciphertext, pem_nonce = EXCLUDED.pem_nonce,
			secret_ciphertext = EXCLUDED.secret_ciphertext, secret_nonce = EXCLUDED.secret_nonce,
			html_url = EXCLUDED.html_url
		RETURNING `+ghAppCols,
		a.OrgID, a.AppID, a.Slug, a.Name,
		a.PEMCiphertext, a.PEMNonce, a.SecretCiphertext, a.SecretNonce,
		a.HTMLURL, a.CreatedBy))
}

// GitHubApps lists an org's registered apps.
func (s *Store) GitHubApps(ctx context.Context, orgID uuid.UUID) ([]GitHubApp, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+ghAppCols+` FROM github_apps WHERE org_id = $1 ORDER BY created_at`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GitHubApp
	for rows.Next() {
		a, err := scanGitHubApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AllGitHubApps lists every registered app across orgs — used for webhook
// signature verification and repo → installation resolution.
func (s *Store) AllGitHubApps(ctx context.Context) ([]GitHubApp, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+ghAppCols+` FROM github_apps ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GitHubApp
	for rows.Next() {
		a, err := scanGitHubApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
