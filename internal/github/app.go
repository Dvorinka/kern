// GitHub App authentication: a JWT signed with the app's RSA key exchanges for
// per-installation access tokens, which then drive the same Client as a PAT.
package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// App authenticates as a GitHub App and mints installation tokens.
type App struct {
	id   string
	key  *rsa.PrivateKey
	base string
	http *http.Client

	mu   sync.Mutex
	inst map[string]int64    // "owner/repo" -> installation id
	toks map[int64]instToken // installation id -> cached token
}

type instToken struct {
	token   string
	expires time.Time
}

// NewApp parses the PEM private key (PKCS#1 or PKCS#8) and returns the app
// credentials. apiBase defaults to https://api.github.com.
func NewApp(id, privateKeyPEM, apiBase string) (*App, error) {
	key, err := parsePrivateKey(privateKeyPEM)
	if err != nil {
		return nil, err
	}
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}
	return &App{
		id: id, key: key, base: strings.TrimRight(apiBase, "/"),
		http: &http.Client{Timeout: 15 * time.Second},
		inst: make(map[string]int64), toks: make(map[int64]instToken),
	}, nil
}

func parsePrivateKey(pemData string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemData))
	if block == nil {
		return nil, fmt.Errorf("github app: private key is not PEM")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("github app: unparseable private key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("github app: private key is not RSA")
	}
	return key, nil
}

// jwt mints an app assertion: RS256, iss=app id, 10-minute window (iat backdated
// a minute for clock skew).
func (a *App) jwt() (string, error) {
	now := time.Now()
	claims, err := json.Marshal(map[string]any{
		"iat": now.Add(-time.Minute).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": a.id,
	})
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	msg := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." +
		enc.EncodeToString(claims)
	digest := sha256.Sum256([]byte(msg))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return msg + "." + enc.EncodeToString(sig), nil
}

func (a *App) do(ctx context.Context, method, path string, out any) error {
	jwt, err := a.jwt()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+jwt)
	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("github %s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// InstallationID resolves a repo to its app installation, cached for the
// process lifetime (installations rarely change).
func (a *App) InstallationID(ctx context.Context, repo string) (int64, error) {
	a.mu.Lock()
	if id, ok := a.inst[repo]; ok {
		a.mu.Unlock()
		return id, nil
	}
	a.mu.Unlock()
	var out struct {
		ID int64 `json:"id"`
	}
	if err := a.do(ctx, http.MethodGet, "/repos/"+repo+"/installation", &out); err != nil {
		return 0, err
	}
	a.mu.Lock()
	a.inst[repo] = out.ID
	a.mu.Unlock()
	return out.ID, nil
}

// token mints or reuses an installation access token until a minute before
// its GitHub-side expiry.
func (a *App) token(ctx context.Context, instID int64) (string, error) {
	a.mu.Lock()
	if t, ok := a.toks[instID]; ok && time.Until(t.expires) > time.Minute {
		a.mu.Unlock()
		return t.token, nil
	}
	a.mu.Unlock()
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	path := fmt.Sprintf("/app/installations/%d/access_tokens", instID)
	if err := a.do(ctx, http.MethodPost, path, &out); err != nil {
		return "", err
	}
	a.mu.Lock()
	a.toks[instID] = instToken{token: out.Token, expires: out.ExpiresAt}
	a.mu.Unlock()
	return out.Token, nil
}

// Client returns a repo-scoped API client backed by an installation token.
func (a *App) Client(ctx context.Context, repo string) (*Client, error) {
	instID, err := a.InstallationID(ctx, repo)
	if err != nil {
		return nil, err
	}
	tok, err := a.token(ctx, instID)
	if err != nil {
		return nil, err
	}
	return New(a.base, tok), nil
}

// Resolver picks the credential for a repo: the app's installation token when
// configured, else the static-token client (PAT; possibly anonymous).
type Resolver struct {
	App *App
	PAT *Client
}

// Client resolves repo to a usable client. Falls back to the PAT client when
// no app is configured.
func (r *Resolver) Client(ctx context.Context, repo string) (*Client, error) {
	if r.App != nil {
		return r.App.Client(ctx, repo)
	}
	if r.PAT != nil {
		return r.PAT, nil
	}
	return nil, fmt.Errorf("no github credentials configured")
}
