// Package api is the KERN control-plane HTTP surface.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/dvorinka/kern/internal/blobs"
	"github.com/dvorinka/kern/internal/config"
	"github.com/dvorinka/kern/internal/geoip"
	"github.com/dvorinka/kern/internal/github"
	"github.com/dvorinka/kern/internal/orchestrator"
	"github.com/dvorinka/kern/internal/secrets"
	"github.com/dvorinka/kern/internal/store"
	"github.com/dvorinka/kern/internal/version"
)

type Server struct {
	Cfg     *config.Config
	Store   *store.Store
	Orch    *orchestrator.Orchestrator
	Blobs   blobs.Store
	Cipher  *secrets.Cipher
	Log     *zap.Logger
	Started time.Time
	Engine  *gin.Engine
	GH      *github.Resolver
	Geo     *geoip.Resolver
	limiter *loginLimiter

	ghAppsMu  sync.Mutex
	ghApps    map[uuid.UUID]*github.App // github_apps.id -> lazily built app
	ghRepoApp map[string]uuid.UUID      // repo -> github_apps.id (probe cache)
	geoSelfMu sync.Mutex                // serializes the control-plane self-lookup
}

func New(cfg *config.Config, st *store.Store, orch *orchestrator.Orchestrator, blobStore blobs.Store,
	cipher *secrets.Cipher, log *zap.Logger) *Server {
	// App credentials, when present and parseable, outrank the PAT: statuses
	// and repo reads then run as the GitHub App installation.
	var app *github.App
	if cfg.GitHub.AppID != "" && cfg.GitHub.PrivateKeyPEM != "" {
		a, err := github.NewApp(cfg.GitHub.AppID, cfg.GitHub.PrivateKeyPEM, cfg.GitHub.APIBaseURL)
		if err != nil {
			log.Error("github app credentials invalid; falling back to token auth", zap.Error(err))
		} else {
			app = a
		}
	}
	s := &Server{
		Cfg: cfg, Store: st, Orch: orch, Blobs: blobStore, Cipher: cipher,
		Log: log, Started: time.Now(), limiter: newLoginLimiter(8, time.Minute),
		GH:     &github.Resolver{App: app, PAT: github.New(cfg.GitHub.APIBaseURL, cfg.GitHub.StatusToken)},
		Geo:    geoip.New(cfg.GeoIPURL),
		ghApps: make(map[uuid.UUID]*github.App), ghRepoApp: make(map[string]uuid.UUID),
	}
	s.wireCommitStatus()
	s.Engine = s.buildRouter()
	return s
}

// wireCommitStatus reports each run's state back to GitHub as a commit status
// and a check run, posted as the App installation when configured, else the
// PAT. A check-run failure is logged and does not undo the status. Runs
// without a repo binding no-op before any client work.
func (s *Server) wireCommitStatus() {
	s.Orch.PostStatus = func(ctx context.Context, run store.Run, state string) {
		if state != "pending" {
			s.notifyRunCompleted(ctx, run)
		}
		var src map[string]any
		_ = json.Unmarshal(run.Source, &src)
		repo, _ := src["repo_full_name"].(string)
		if repo == "" || run.SHA == "" {
			return
		}
		cl, err := s.githubClient(ctx, repo)
		if err != nil {
			s.Log.Warn("github client", zap.String("repo", repo), zap.Error(err))
			return
		}
		jobs, jerr := s.Store.JobsForRun(ctx, run.ID)
		if jerr != nil {
			s.Log.Warn("jobs for github status", zap.Error(jerr))
			jobs = nil
		}
		target := s.runPageURL(ctx, run)
		if err := cl.PostCommitStatus(ctx, repo, run.SHA, state,
			"kern/"+run.WorkflowName, target, statusDescription(run, state, jobs)); err != nil {
			s.Log.Warn("commit status failed", zap.String("repo", repo), zap.Error(err))
		}
		status, conclusion := githubCheck(state, run)
		if err := cl.UpsertCheckRun(ctx, repo, github.CheckRun{
			Name: "kern/" + run.WorkflowName, HeadSHA: run.SHA,
			Status: status, Conclusion: conclusion, DetailsURL: target,
			ExternalID: run.ID.String(), Title: checkTitle(run, state),
			Summary: checkSummary(run, jobs),
		}); err != nil {
			s.Log.Warn("check run failed", zap.String("repo", repo), zap.Error(err))
		}
	}
}

func (s *Server) buildRouter() *gin.Engine {
	if s.Cfg.Env == "production" {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	r.Use(gin.Recovery(), s.requestLogger(), s.securityHeaders(), s.cors())
	r.MaxMultipartMemory = 8 << 20

	r.GET("/healthz", s.health)
	r.GET("/readyz", s.ready)
	r.GET("/api/v1/meta", s.meta)
	r.POST("/api/v1/auth/login", s.login)
	r.POST("/api/v1/auth/token", s.cliToken)
	r.POST("/api/v1/webhooks/github", s.githubWebhook)
	r.GET("/api/v1/public/runs/:id", s.getPublicRun)

	api := r.Group("/api/v1")
	// auth: any org member; API tokens need the read scope.
	// write: member write operations; tokens need the write scope.
	// admin: org admin or owner role AND the admin token scope. Sessions
	// carry no scopes, so the role check is what constrains the dashboard.
	auth := api.Group("", s.requireAuth(), s.requireScope("read"))
	write := api.Group("", s.requireAuth(), s.requireScope("write"))
	admin := api.Group("", s.requireAuth(), s.requireRole("admin"), s.requireScope("admin"))
	{
		auth.POST("/auth/logout", s.logout)
		auth.GET("/auth/me", s.me)
		auth.GET("/auth/tokens", s.listTokens)
		write.POST("/auth/tokens", s.createToken)
		write.DELETE("/auth/tokens/:id", s.deleteToken)

		auth.GET("/overview", s.overview)
		auth.GET("/projects", s.listProjects)
		admin.POST("/projects", s.createProject)
		auth.GET("/projects/:project", s.getProject)
		write.POST("/projects/:project/sync", s.syncProject)
		write.PUT("/projects/:project/source", s.uploadSource)
		auth.GET("/projects/:project/source/resolve", s.resolveSource)
		auth.GET("/projects/:project/workflows", s.listWorkflows)
		write.POST("/projects/:project/workflows/:workflowID/runs", s.triggerRun)
		admin.GET("/github-app/manifest", s.githubAppManifest)
		admin.GET("/github-app/callback", s.githubAppCallback)

		auth.GET("/runs", s.listRuns)
		auth.GET("/runs/:id", s.getRun)
		write.POST("/runs/:id/cancel", s.cancelRun)
		admin.POST("/runs/:id/approve", s.approveRun)
		write.POST("/runs/:id/rerun", s.rerunRun)
		auth.GET("/steps/:stepID/logs", s.stepLogs)
		auth.GET("/steps/:stepID/logs/stream", s.stepLogStream)
		auth.GET("/steps/:stepID/summary", s.stepSummary)

		auth.GET("/runners", s.listRunners)
		admin.POST("/enroll-tokens", s.createEnrollToken)
		admin.POST("/runners/:id/drain", s.drainRunner)
		admin.PATCH("/runners/:id/geo", s.patchRunnerGeo)
		admin.DELETE("/runners/:id", s.deleteRunner)

		auth.GET("/instance/map", s.fleetMap)
		admin.PATCH("/instance/geo", s.patchControlGeo)

		auth.GET("/cache", s.listCache)
		write.DELETE("/cache/:id", s.deleteCache)

		auth.GET("/webhooks/deliveries", s.listWebhookDeliveries)
		auth.GET("/webhooks/deliveries/:id", s.getWebhookDelivery)

		auth.GET("/projects/:project/environments", s.listEnvironments)
		admin.PUT("/projects/:project/environments/:name", s.putEnvironment)
		admin.DELETE("/projects/:project/environments/:name", s.deleteEnvironment)
		auth.GET("/deployments", s.listDeployments)
		write.POST("/jobs/:id/approve", s.approveDeployment)
		write.POST("/jobs/:id/reject", s.rejectDeployment)

		admin.GET("/members", s.listMembers)
		admin.POST("/members", s.createMember)
		admin.PATCH("/members/:id", s.patchMember)
		admin.DELETE("/members/:id", s.deleteMember)

		auth.GET("/settings", s.getOrgSettings)
		admin.PUT("/settings", s.putOrgSettings)
		auth.GET("/usage", s.usage)
		auth.GET("/notifications", s.notifications)
		admin.GET("/audit", s.listAudit)
		auth.GET("/instance/update", s.updateCheck)
		auth.GET("/metrics", s.prometheus)
		auth.POST("/mcp", s.mcp)
		auth.GET("/projects/:project/settings", s.getProjectSettings)
		admin.PUT("/projects/:project/settings", s.putProjectSettings)
		write.POST("/jobs/:id/remediate", s.approveRemediation)
		write.POST("/jobs/:id/remediate-dismiss", s.dismissRemediation)

		auth.GET("/secrets", s.listSecrets)
		admin.PUT("/secrets/:name", s.putSecret)
		admin.DELETE("/secrets/:name", s.deleteSecret)
	}

	r.POST("/api/v1/runners/register", s.registerRunner)
	r.GET("/api/v1/update/plan", s.updatePlan)
	r.POST("/api/v1/update/status", s.updateStatusReport)
	runner := r.Group("/api/v1", s.requireRunner())
	{
		runner.POST("/runners/heartbeat", s.runnerHeartbeat)
		runner.POST("/runners/claim", s.claimJob)
		runner.POST("/runners/jobs/:jobID/heartbeat", s.renewJobLease)
		runner.POST("/runners/jobs/:jobID/steps/:n/start", s.startStep)
		runner.POST("/runners/jobs/:jobID/steps/:n/logs", s.appendLog)
		runner.POST("/runners/jobs/:jobID/steps/:n/finish", s.finishStep)
		runner.PUT("/runners/jobs/:jobID/steps/:n/summary", s.uploadStepSummary)
		runner.POST("/runners/jobs/:jobID/finish", s.finishJob)
		runner.GET("/runners/jobs/:jobID/cancel-check", s.cancelCheck)
		runner.GET("/runners/jobs/:jobID/secrets", s.jobSecrets)
		runner.GET("/runners/jobs/:jobID/source", s.jobSource)
		runner.GET("/runners/jobs/:jobID/artifacts", s.jobArtifacts)
		runner.PUT("/runners/jobs/:jobID/artifacts/:name", s.uploadArtifact)
		runner.GET("/runners/jobs/:jobID/artifacts/:name", s.downloadArtifact)
		runner.GET("/runners/jobs/:jobID/cache/restore", s.cacheRestore)
		runner.PUT("/runners/jobs/:jobID/cache/:key", s.cacheSave)
	}

	auth.GET("/artifacts", s.listArtifacts)
	auth.GET("/artifacts/:id/download", s.getArtifact)
	write.DELETE("/artifacts/:id", s.deleteArtifact)
	if s.Cfg.WebDir != "" {
		r.NoRoute(spaHandler(s.Cfg.WebDir))
	}
	return r
}

// spaHandler serves a built single-page app: real files verbatim, everything
// else falls back to index.html so client-side routes survive reloads.
func spaHandler(root string) gin.HandlerFunc {
	fs := http.Dir(root)
	index := filepath.Join(root, "index.html")
	return func(c *gin.Context) {
		p := path.Clean("/" + c.Request.URL.Path)
		if strings.HasPrefix(p, "/api/") {
			c.Status(http.StatusNotFound)
			return
		}
		if f, err := fs.Open(p); err == nil {
			st, serr := f.Stat()
			if serr == nil && !st.IsDir() {
				defer f.Close()
				http.ServeContent(c.Writer, c.Request, st.Name(), st.ModTime(), f)
				return
			}
			f.Close()
		}
		c.File(index)
	}
}

func (s *Server) requestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		path := c.FullPath()
		if path == "" {
			path = c.Request.URL.Path
		}
		if strings.HasSuffix(path, "/logs") && c.Request.Method == http.MethodPost {
			// Log body ingestion is high volume; record only the byte count.
			s.Log.Debug("request", zap.String("method", c.Request.Method),
				zap.String("path", path), zap.Int("status", c.Writer.Status()),
				zap.Duration("took", time.Since(start)))
			return
		}
		s.Log.Info("request",
			zap.String("method", c.Request.Method),
			zap.String("path", path),
			zap.Int("status", c.Writer.Status()),
			zap.Duration("took", time.Since(start)))
	}
}

func (s *Server) cors() gin.HandlerFunc {
	allowed := map[string]bool{}
	for _, o := range s.Cfg.AllowedOrigins {
		allowed[o] = true
	}
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" && (allowed[origin] || s.Cfg.Env == "development") {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Credentials", "true")
			c.Header("Vary", "Origin")
			c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")
			c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

func fail(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, gin.H{"error": apiError{Code: code, Message: message}})
}

func (s *Server) internalError(c *gin.Context, err error) {
	if errors.Is(err, store.ErrNotFound) {
		fail(c, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	s.Log.Error("request failed", zap.String("path", c.FullPath()), zap.Error(err))
	fail(c, http.StatusInternalServerError, "internal", "internal server error")
}

func (s *Server) health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok", "uptime_seconds": int(time.Since(s.Started).Seconds())})
}

func (s *Server) ready(c *gin.Context) {
	ctx, cancel := contextWithTimeout(c, 2*time.Second)
	defer cancel()
	if err := s.Store.Pool().Ping(ctx); err != nil {
		fail(c, http.StatusServiceUnavailable, "database_unavailable", "database is not reachable")
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ready"})
}

func (s *Server) meta(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"name":    "KERN",
		"version": version.Version,
		"env":     s.Cfg.Env,
	})
}
