package api

import (
	"context"
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
				"contents":      "read",
				"metadata":      "read",
				"statuses":      "write",
				"checks":        "write",
				"pull_requests": "read",
			},
			// pull_request_target is not a subscribable App event. The webhook
			// handler synthesizes it from pull_request deliveries.
			"default_events": []string{"push", "pull_request", "check_run", "check_suite"},
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
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "github_app.remove", id.String())
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// githubAppCallback converts the manifest code into app credentials, stores
// them encrypted under the caller's org, then sends the browser to GitHub's
// install flow so the user picks which repos the app can see.
func (s *Server) githubAppCallback(c *gin.Context) {
	user, _ := currentUser(c)
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

// githubClient resolves a repo to an API client: the env-configured app when
// installed, then any registered org app, then the PAT.
func (s *Server) githubClient(ctx context.Context, repo string) (*github.Client, error) {
	if s.GH.App != nil {
		if cl, err := s.GH.App.Client(ctx, repo); err == nil {
			return cl, nil
		}
	}
	if cl, err := s.dbAppClient(ctx, repo); cl != nil || err != nil {
		return cl, err
	}
	if s.GH.PAT != nil {
		return s.GH.PAT, nil
	}
	return nil, fmt.Errorf("no github credentials configured")
}

// dbAppClient finds a registered org app installed on repo. Successful
// repo → app resolutions are cached for the process lifetime.
func (s *Server) dbAppClient(ctx context.Context, repo string) (*github.Client, error) {
	s.ghAppsMu.Lock()
	if id, ok := s.ghRepoApp[repo]; ok {
		app := s.ghApps[id]
		s.ghAppsMu.Unlock()
		if app != nil {
			return app.Client(ctx, repo)
		}
	} else {
		s.ghAppsMu.Unlock()
	}
	rows, err := s.Store.AllGitHubApps(ctx)
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
		s.ghRepoApp[repo] = row.ID
		s.ghAppsMu.Unlock()
		return app.Client(ctx, repo)
	}
	return nil, nil
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

// webhookSecrets collects every secret a GitHub delivery might be signed with:
// the env secret plus each registered app's webhook secret.
func (s *Server) webhookSecrets(ctx context.Context) []string {
	var out []string
	if s.Cfg.GitHub.WebhookSecret != "" {
		out = append(out, s.Cfg.GitHub.WebhookSecret)
	}
	rows, err := s.Store.AllGitHubApps(ctx)
	if err != nil {
		return out
	}
	for _, r := range rows {
		if sec, err := s.Cipher.Decrypt(r.SecretCiphertext, r.SecretNonce); err == nil && len(sec) > 0 {
			out = append(out, sec)
		}
	}
	return out
}
