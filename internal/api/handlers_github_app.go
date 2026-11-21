package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/dvorinka/kern/internal/github"
	"github.com/dvorinka/kern/internal/store"
)

// githubAppManifest returns the manifest JSON the browser POSTs to GitHub's
// app-creation form. GitHub then redirects back to githubAppCallback with a
// one-time conversion code.
func (s *Server) githubAppManifest(c *gin.Context) {
	user, _ := currentUser(c)
	// CSRF nonce for the manifest callback: the browser returns from GitHub's
	// app-creation page via a top-level GET, so a SameSite=Lax cookie rides
	// along and proves the flow was started by this user (#368).
	nonce, err := randomHex(16)
	if err != nil {
		s.internalError(c, err)
		return
	}
	s.ghAppsMu.Lock()
	s.ghAppStates[user.ID] = ghAppState{nonce: nonce, expiry: time.Now().Add(10 * time.Minute)}
	s.ghAppsMu.Unlock()
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie("kern_gh_app_state", nonce, 600, "/", "", s.Cfg.Env == "production", true)
	base := s.Cfg.PublicBaseURL
	// GitHub app names are globally unique; a fixed name fails the manifest
	// POST once an app with that name exists on the account.
	name := fmt.Sprintf("kern-ci-%s", user.OrgID.String()[:8])
	c.JSON(http.StatusOK, gin.H{
		"url": "https://github.com/settings/apps/new",
		"manifest": gin.H{
			"name":         name,
			"url":          base,
			"public":       false,
			"redirect_url": base + "/api/v1/github-app/callback",
			"hook_attributes": gin.H{
				"url":    base + "/api/v1/webhooks/github",
				"active": true,
			},
			"default_permissions": gin.H{
				// contents:write — trusted runs push tags/commits with the
				// per-run installation token (actions/checkout parity). Read
				// leaves `git push` at a 403 after #112's credential persist.
				"contents": "write",
				"metadata": "read",
				"statuses": "write",
				"checks":   "write",
				// actions:write — trusted workflows dispatch follow-up runs
				// (`gh workflow run`, reusable-workflow APIs) with the per-run
				// token. Without it the dispatch returns 403 "Resource not
				// accessible by integration" (#322). Apps created before this
				// grant must accept the permission update in GitHub settings.
				"actions":       "write",
				"pull_requests": "read",
			},
			// pull_request_target is not a subscribable App event. The webhook
			// handler synthesizes it from pull_request deliveries.
			"default_events": []string{"push", "pull_request", "check_run", "check_suite", "workflow_run", "release", "repository_dispatch"},
		},
	})
}

// githubAppStatus reports which GitHub credential sources are live: the
// env-configured app (app id + key file) and the org's registered apps from
// the manifest flow. Secrets never leave the server.
func (s *Server) githubAppStatus(c *gin.Context) {
	user, _ := currentUser(c)
	apps, err := s.Store.GitHubApps(c.Request.Context(), user.OrgID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	type appView struct {
		ID        string    `json:"id"`
		Slug      string    `json:"slug"`
		Name      string    `json:"name"`
		HTMLURL   string    `json:"html_url"`
		CreatedAt time.Time `json:"created_at"`
	}
	views := make([]appView, 0, len(apps))
	for _, a := range apps {
		views = append(views, appView{ID: a.ID.String(), Slug: a.Slug, Name: a.Name,
			HTMLURL: a.HTMLURL, CreatedAt: a.CreatedAt})
	}
	var envApp any
	if s.GH.App != nil {
		envApp = gin.H{"app_id": s.Cfg.GitHub.AppID}
	}
	c.JSON(http.StatusOK, gin.H{"env_app": envApp, "apps": views})
}

// deleteGitHubApp disconnects a manifest-created app from the org. The GitHub
// side is untouched — the app keeps existing, KERN just stops using it.
func (s *Server) deleteGitHubApp(c *gin.Context) {
	user, _ := currentUser(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_id", "app id must be a uuid")
		return
	}
	if err := s.Store.DeleteGitHubApp(c.Request.Context(), user.OrgID, id); err != nil {
		s.internalError(c, err)
		return
	}
	// Evict the in-memory app and every repo→app probe that resolved to it —
	// a deleted app must stop serving credentials immediately (#368).
	s.ghAppsMu.Lock()
	delete(s.ghApps, id)
	for key, appID := range s.ghRepoApp {
		if appID == id {
			delete(s.ghRepoApp, key)
		}
	}
	s.ghAppsMu.Unlock()
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "github_app.remove", id.String())
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// githubAppCallback converts the manifest code into app credentials, stores
// them encrypted under the caller's org, then sends the browser to GitHub's
// install flow so the user picks which repos the app can see.
func (s *Server) githubAppCallback(c *gin.Context) {
	user, _ := currentUser(c)
	// The callback is a GET GitHub redirects the browser to — without the
	// manifest-issued nonce an attacker could bind their own app's
	// credentials to a victim org (#368). The nonce is single-use.
	stateCookie, _ := c.Cookie("kern_gh_app_state")
	s.ghAppsMu.Lock()
	st, pending := s.ghAppStates[user.ID]
	delete(s.ghAppStates, user.ID)
	s.ghAppsMu.Unlock()
	if !pending || time.Now().After(st.expiry) || stateCookie == "" || stateCookie != st.nonce {
		fail(c, http.StatusForbidden, "bad_state", "github app manifest state mismatch or expired; restart the flow")
		return
	}
	code := c.Query("code")
	if code == "" {
		fail(c, http.StatusBadRequest, "missing_code", "GitHub returned no conversion code")
		return
	}
	conv, err := github.New(s.Cfg.GitHub.APIBaseURL, "").ConvertManifestCode(c.Request.Context(), code)
	if err != nil {
		fail(c, http.StatusBadGateway, "github_conversion_failed", err.Error())
		return
	}
	pemCT, pemN, err := s.Cipher.Encrypt(conv.PEM)
	if err != nil {
		s.internalError(c, err)
		return
	}
	secCT, secN, err := s.Cipher.Encrypt(conv.WebhookSecret)
	if err != nil {
		s.internalError(c, err)
		return
	}
	row := store.GitHubApp{
		OrgID: user.OrgID, AppID: conv.ID, Slug: conv.Slug, Name: conv.Name,
		PEMCiphertext: pemCT, PEMNonce: pemN,
		SecretCiphertext: secCT, SecretNonce: secN,
		HTMLURL: conv.HTMLURL, CreatedBy: user.ID,
	}
	stored, err := s.Store.PutGitHubApp(c.Request.Context(), row)
	if err != nil {
		s.internalError(c, err)
		return
	}
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "github_app.register", conv.Slug)
	c.Redirect(http.StatusFound, fmt.Sprintf("https://github.com/apps/%s/installations/new", stored.Slug))
}

// githubClient resolves a repo to an API client without org preference —
// used by org-less internal callers (webhook ref resolution, reusable
// workflow fetches for foreign repos). Prefer githubClientForOrg.
func (s *Server) githubClient(ctx context.Context, repo string) (*github.Client, error) {
	return s.githubClientForOrg(ctx, uuid.Nil, repo)
}

// githubClientForOrg resolves a repo to an API client: the env-configured
// app when installed, then a GitHub App registered by orgID, then the PAT.
// orgID Nil falls back to any registered app (webhook resolution happens
// before tenant binding).
func (s *Server) githubClientForOrg(ctx context.Context, orgID uuid.UUID, repo string) (*github.Client, error) {
	if s.GH.App != nil {
		if cl, err := s.GH.App.Client(ctx, repo); err == nil {
			return cl, nil
		}
	}
	if cl, err := s.dbAppClient(ctx, orgID, repo); cl != nil || err != nil {
		return cl, err
	}
	if s.GH.PAT != nil {
		return s.GH.PAT, nil
	}
	return nil, fmt.Errorf("no github credentials configured")
}

// githubAppForOrg returns the GitHub App able to mint repo tokens for orgID:
// the env-configured app when installed on the repo, else an app the org
// registered through the manifest flow. Nil when nothing authenticates —
// callers then mint no token rather than falling back to wider credentials.
func (s *Server) githubAppForOrg(ctx context.Context, orgID uuid.UUID, repo string) *github.App {
	if s.GH.App != nil {
		if _, err := s.GH.App.InstallationID(ctx, repo); err == nil {
			return s.GH.App
		}
	}
	app, err := s.dbAppForRepo(ctx, orgID, repo)
	if err != nil {
		s.Log.Warn("github app lookup failed", zap.String("repo", repo), zap.Error(err))
		return nil
	}
	return app
}

// dbAppClient finds a registered org app installed on repo.
func (s *Server) dbAppClient(ctx context.Context, orgID uuid.UUID, repo string) (*github.Client, error) {
	app, err := s.dbAppForRepo(ctx, orgID, repo)
	if app == nil || err != nil {
		return nil, err
	}
	return app.Client(ctx, repo)
}

// dbAppForRepo finds a GitHub App registered by orgID that is installed on
// repo. Successful resolutions are cached per org+repo so one tenant's app
// can never be selected for another tenant's repository (#368).
func (s *Server) dbAppForRepo(ctx context.Context, orgID uuid.UUID, repo string) (*github.App, error) {
	key := orgID.String() + "|" + repo
	s.ghAppsMu.Lock()
	if id, ok := s.ghRepoApp[key]; ok {
		app := s.ghApps[id]
		s.ghAppsMu.Unlock()
		if app != nil {
			return app, nil
		}
	} else {
		s.ghAppsMu.Unlock()
	}
	var rows []store.GitHubApp
	var err error
	if orgID == uuid.Nil {
		rows, err = s.Store.AllGitHubApps(ctx)
	} else {
		rows, err = s.Store.GitHubApps(ctx, orgID)
	}
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		app, err := s.appForRow(row)
		if err != nil {
			s.Log.Warn("github app unusable", zap.String("slug", row.Slug), zap.Error(err))
			continue
		}
		if _, err := app.InstallationID(ctx, repo); err != nil {
			continue
		}
		s.ghAppsMu.Lock()
		s.ghRepoApp[key] = row.ID
		s.ghAppsMu.Unlock()
		return app, nil
	}
	return nil, nil
}

// randomHex returns n crypto-random bytes hex-encoded.
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// appForRow lazily builds and caches a github.App from its encrypted pem.
func (s *Server) appForRow(row store.GitHubApp) (*github.App, error) {
	s.ghAppsMu.Lock()
	defer s.ghAppsMu.Unlock()
	if app, ok := s.ghApps[row.ID]; ok {
		return app, nil
	}
	pemData, err := s.Cipher.Decrypt(row.PEMCiphertext, row.PEMNonce)
	if err != nil {
		return nil, err
	}
	app, err := github.NewApp(strconv.FormatInt(row.AppID, 10), pemData, s.Cfg.GitHub.APIBaseURL)
	if err != nil {
		return nil, err
	}
	s.ghApps[row.ID] = app
	return app, nil
}

// webhookSecret is one accepted webhook signing secret plus the org it
// belongs to. The env-configured secret has no org (uuid.Nil) — deliveries
// it verifies are bound later, when the payload's repo resolves to a
// project.
type webhookSecret struct {
	value string
	orgID uuid.UUID
}

// webhookSecrets collects every secret a GitHub delivery might be signed with:
// the env secret plus each registered app's webhook secret, tagged with the
// app's owning org so deliveries can be tenant-scoped (#368).
func (s *Server) webhookSecrets(ctx context.Context) []webhookSecret {
	var out []webhookSecret
	if s.Cfg.GitHub.WebhookSecret != "" {
		out = append(out, webhookSecret{value: s.Cfg.GitHub.WebhookSecret})
	}
	rows, err := s.Store.AllGitHubApps(ctx)
	if err != nil {
		return out
	}
	for _, r := range rows {
		if sec, err := s.Cipher.Decrypt(r.SecretCiphertext, r.SecretNonce); err == nil && len(sec) > 0 {
			out = append(out, webhookSecret{value: sec, orgID: r.OrgID})
		}
	}
	return out
}

// deliveryOrg resolves which org a verified delivery belongs to: the org of
// the project bound to the payload's repository when exactly one org owns
// it, else the org of the app whose secret verified the signature.
func (s *Server) deliveryOrg(ctx context.Context, body []byte, matchedOrg *uuid.UUID) (uuid.UUID, bool) {
	var p struct {
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if json.Unmarshal(body, &p) != nil || p.Repository.FullName == "" {
		return uuid.Nil, false
	}
	projects, err := s.Store.ProjectsByRepoFullName(ctx, p.Repository.FullName)
	if err != nil || len(projects) == 0 {
		return uuid.Nil, false
	}
	org := projects[0].OrgID
	for _, pr := range projects[1:] {
		if pr.OrgID != org {
			// The repo name maps to projects in multiple orgs — ambiguous,
			// fall back to the verifying credential's org.
			if matchedOrg != nil {
				return *matchedOrg, true
			}
			return uuid.Nil, false
		}
	}
	return org, true
}
