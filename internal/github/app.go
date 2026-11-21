// GitHub App authentication: a JWT signed with the app's RSA key exchanges for
// per-installation access tokens, which then drive the same Client as a PAT.
package github

import (
	"bytes"
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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dvorinka/kern/internal/telemetry"
)

// App authenticates as a GitHub App and mints installation tokens.
type App struct {
	id   string
	key  *rsa.PrivateKey
	base string
	http *http.Client

	mu       sync.Mutex
	inst     map[string]int64             // "owner/repo" -> installation id
	instPerm map[string]map[string]string // "owner/repo" -> granted permissions
	instAt   map[string]time.Time         // "owner/repo" -> when inst/instPerm were fetched
	toks     map[string]instToken         // scope key -> cached token
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
		inst: make(map[string]int64), instPerm: make(map[string]map[string]string),
		instAt: make(map[string]time.Time),
		toks:   make(map[string]instToken),
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
	return a.doBody(ctx, method, path, nil, out)
}

func (a *App) doBody(ctx context.Context, method, path string, body any, out any) error {
	jwt, err := a.jwt()
	if err != nil {
		return err
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+jwt)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.http.Do(req)
	if err != nil {
		telemetry.IncGitHubAPI("error")
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		telemetry.IncGitHubAPI("error")
		return fmt.Errorf("github %s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	telemetry.IncGitHubAPI("ok")
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// installation resolves a repo to its app installation id and the permission
// set the installation grants. The pair is cached for instCacheTTL — long
// enough to keep token mints cheap, short enough that an installation
// permission upgrade reaches RunToken without an api restart (a lifetime
// cache once pinned a repo to contents:read for hours after the upgrade).
const instCacheTTL = 15 * time.Minute

func (a *App) installation(ctx context.Context, repo string) (int64, map[string]string, error) {
	a.mu.Lock()
	if id, ok := a.inst[repo]; ok && time.Since(a.instAt[repo]) < instCacheTTL {
		perms := a.instPerm[repo]
		a.mu.Unlock()
		return id, perms, nil
	}
	a.mu.Unlock()
	var out struct {
		ID          int64             `json:"id"`
		Permissions map[string]string `json:"permissions"`
	}
	if err := a.do(ctx, http.MethodGet, "/repos/"+repo+"/installation", &out); err != nil {
		return 0, nil, err
	}
	a.mu.Lock()
	a.inst[repo] = out.ID
	a.instPerm[repo] = out.Permissions
	a.instAt[repo] = time.Now()
	a.mu.Unlock()
	return out.ID, out.Permissions, nil
}

// InstallationID resolves a repo to its app installation, cached for
// instCacheTTL alongside the permission set.
func (a *App) InstallationID(ctx context.Context, repo string) (int64, error) {
	id, _, err := a.installation(ctx, repo)
	return id, err
}

// token mints or reuses an installation access token until a minute before
// its GitHub-side expiry. The unscoped form is used for server-side API
// clients only — tokens handed to runners always go through CloneToken or
// RunToken, which pin repositories and permissions.
func (a *App) token(ctx context.Context, instID int64) (string, error) {
	return a.mintToken(ctx, instID, nil, nil)
}

// mintToken posts to the installation access_tokens endpoint. repos scopes
// the token to named repositories (nil = every repo the installation covers);
// perms is the permission ceiling — nil omits the permissions object so
// GitHub grants the installation's defaults, while a non-nil map (even an
// empty one, GitHub's `permissions: {}` = zero scope) is sent verbatim after
// normalization. Tokens are cached per scope until a minute before expiry.
func (a *App) mintToken(ctx context.Context, instID int64, repos []string, perms map[string]string) (string, error) {
	key := tokenScopeKey(instID, repos, perms)
	a.mu.Lock()
	if t, ok := a.toks[key]; ok && time.Until(t.expires) > time.Minute {
		a.mu.Unlock()
		return t.token, nil
	}
	a.mu.Unlock()
	body := map[string]any{}
	if len(repos) > 0 {
		body["repositories"] = repos
	}
	if perms != nil {
		normalized := make(map[string]string, len(perms))
		for k, v := range perms {
			normalized[githubPermName(k)] = v
		}
		body["permissions"] = normalized
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	path := fmt.Sprintf("/app/installations/%d/access_tokens", instID)
	if err := a.doBody(ctx, http.MethodPost, path, body, &out); err != nil {
		return "", err
	}
	a.mu.Lock()
	a.toks[key] = instToken{token: out.Token, expires: out.ExpiresAt}
	a.mu.Unlock()
	return out.Token, nil
}

// tokenScopeKey builds the cache key for a scoped token mint. Permission
// entries are sorted so map iteration order cannot split the cache.
func tokenScopeKey(instID int64, repos []string, perms map[string]string) string {
	var b strings.Builder
	b.WriteString(strconv.FormatInt(instID, 10))
	for _, r := range repos {
		b.WriteByte('|')
		b.WriteString(r)
	}
	if perms != nil {
		keys := make([]string, 0, len(perms))
		for k := range perms {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if len(keys) == 0 {
			// Distinguish "permissions: {}" (zero scope) from no permissions
			// object at all — they must not share a cache entry.
			b.WriteString("|{}")
		}
		for _, k := range keys {
			b.WriteByte('|')
			b.WriteString(githubPermName(k))
			b.WriteByte('=')
			b.WriteString(perms[k])
		}
	}
	return b.String()
}

// githubPermName maps workflow-syntax permission names onto the GitHub REST
// vocabulary: Actions writes `pull-requests`/`id-token` with hyphens while
// the API expects `pull_requests`/`id_token`.
func githubPermName(name string) string {
	return strings.ReplaceAll(name, "-", "_")
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

// CloneToken returns an installation access token for git clone auth,
// scoped to the single repository with contents:read — the minimum a clone
// needs, safe to hand to untrusted fork/PR jobs. It is minted at claim time,
// never stored, and expires about an hour later.
func (a *App) CloneToken(ctx context.Context, repo string) (string, error) {
	instID, err := a.InstallationID(ctx, repo)
	if err != nil {
		return "", err
	}
	name := repo
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		name = repo[i+1:]
	}
	return a.mintToken(ctx, instID, []string{name}, map[string]string{"contents": "read"})
}

// RunToken mints a per-run installation token scoped to a single repository
// with an explicit permission ceiling — KERN's GITHUB_TOKEN. requested uses
// GitHub's permission vocabulary (read|write|admin|none) in either Actions
// hyphenated or REST underscore spelling; keys are normalized before the
// ceiling is computed. The minted set is the intersection with what the
// installation actually grants, so a workflow can only narrow, never widen.
// A nil requested map omits the permissions object entirely (installation
// defaults, GitHub's permissive-default parity); a non-nil map — including
// an empty one — is honoured literally, so `permissions: {}` yields a
// zero-scope token instead of silently escalating to full grants.
func (a *App) RunToken(ctx context.Context, repo string, requested map[string]string) (token string, permScoped bool, err error) {
	instID, appPerms, err := a.installation(ctx, repo)
	if err != nil {
		return "", false, err
	}
	name := repo
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		name = repo[i+1:]
	}
	var effective map[string]string
	if requested != nil {
		effective = map[string]string{}
		for k, v := range requested {
			if v == "none" {
				continue // omitted keys carry no access on this endpoint
			}
			nk := githubPermName(k)
			if appPerms == nil {
				effective[nk] = v // grants unknown; let GitHub validate
				continue
			}
			appLevel, ok := appPerms[nk]
			if !ok || appLevel == "none" {
				continue // the installation does not grant this permission at all
			}
			// Clamp to the installation's level: a workflow may narrow the
			// ceiling, never widen it past what the app holds.
			if permRank(v) > permRank(appLevel) {
				effective[nk] = appLevel
			} else {
				effective[nk] = v
			}
		}
		if len(requested) > 0 && len(effective) == 0 {
			return "", false, fmt.Errorf("permissions ceiling grants nothing the installation holds")
		}
	}
	tok, err := a.mintToken(ctx, instID, []string{name}, effective)
	if err != nil {
		return "", false, err
	}
	return tok, requested != nil, nil
}

// permRank orders GitHub permission levels for ceiling clamping.
func permRank(level string) int {
	switch level {
	case "admin":
		return 3
	case "write":
		return 2
	case "read":
		return 1
	default:
		return 0
	}
}

// Resolver holds the env-level credentials: the app when configured, else the
// static-token client (PAT; possibly anonymous). Repo-level resolution across
// registered apps lives in the api layer.
type Resolver struct {
	App *App
	PAT *Client
}
