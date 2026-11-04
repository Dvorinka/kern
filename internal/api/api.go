// Package api is the KERN control-plane HTTP surface.
package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/dvorinka/kern/internal/blobs"
	"github.com/dvorinka/kern/internal/config"
	"github.com/dvorinka/kern/internal/orchestrator"
	"github.com/dvorinka/kern/internal/secrets"
	"github.com/dvorinka/kern/internal/store"
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
	limiter *loginLimiter
}

func New(cfg *config.Config, st *store.Store, orch *orchestrator.Orchestrator, blobStore blobs.Store,
	cipher *secrets.Cipher, log *zap.Logger) *Server {
	s := &Server{
		Cfg: cfg, Store: st, Orch: orch, Blobs: blobStore, Cipher: cipher,
		Log: log, Started: time.Now(), limiter: newLoginLimiter(8, time.Minute),
	}
	s.Engine = s.buildRouter()
	return s
}

func (s *Server) buildRouter() *gin.Engine {
	if s.Cfg.Env == "production" {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	r.Use(gin.Recovery(), s.requestLogger(), s.cors())
	r.MaxMultipartMemory = 8 << 20

	r.GET("/healthz", s.health)
	r.GET("/readyz", s.ready)
	r.GET("/api/v1/meta", s.meta)
	r.GET("/api/v1/metrics", s.prometheus)
	r.POST("/api/v1/auth/login", s.login)
	r.POST("/api/v1/auth/token", s.cliToken)
	r.POST("/api/v1/webhooks/github", s.githubWebhook)

	api := r.Group("/api/v1")
	auth := api.Group("", s.requireAuth())
	{
		auth.POST("/auth/logout", s.logout)
		auth.GET("/auth/me", s.me)
		auth.GET("/auth/tokens", s.listTokens)
		auth.POST("/auth/tokens", s.createToken)
		auth.DELETE("/auth/tokens/:id", s.deleteToken)

		auth.GET("/overview", s.overview)
		auth.GET("/projects", s.listProjects)
		auth.POST("/projects", s.createProject)
		auth.GET("/projects/:project", s.getProject)
		auth.POST("/projects/:project/sync", s.syncProject)
		auth.PUT("/projects/:project/source", s.uploadSource)
		auth.GET("/projects/:project/workflows", s.listWorkflows)
		auth.POST("/projects/:project/workflows/:workflowID/runs", s.triggerRun)

		auth.GET("/runs", s.listRuns)
		auth.GET("/runs/:id", s.getRun)
		auth.POST("/runs/:id/cancel", s.cancelRun)
		auth.POST("/runs/:id/rerun", s.rerunRun)
		auth.GET("/steps/:stepID/logs", s.stepLogs)
		auth.GET("/steps/:stepID/logs/stream", s.stepLogStream)
		auth.GET("/steps/:stepID/summary", s.stepSummary)

		auth.GET("/runners", s.listRunners)
		auth.POST("/enroll-tokens", s.createEnrollToken)
		auth.POST("/runners/:id/drain", s.drainRunner)
		auth.DELETE("/runners/:id", s.deleteRunner)

		auth.GET("/cache", s.listCache)
		auth.DELETE("/cache/:id", s.deleteCache)

		auth.GET("/settings", s.getOrgSettings)
		auth.PUT("/settings", s.putOrgSettings)
		auth.GET("/projects/:project/settings", s.getProjectSettings)
		auth.PUT("/projects/:project/settings", s.putProjectSettings)
		auth.POST("/jobs/:id/remediate", s.approveRemediation)

		auth.GET("/secrets", s.listSecrets)
		auth.PUT("/secrets/:name", s.putSecret)
		auth.DELETE("/secrets/:name", s.deleteSecret)
	}

	r.POST("/api/v1/runners/register", s.registerRunner)
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
	auth.DELETE("/artifacts/:id", s.deleteArtifact)
	return r
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
		"version": version,
		"env":     s.Cfg.Env,
	})
}

const version = "0.1.0"
