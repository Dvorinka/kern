package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"go.uber.org/zap"

	"github.com/dvorinka/kern/internal/auth"
	"github.com/dvorinka/kern/internal/store"
	"github.com/dvorinka/kern/internal/version"
)

func (s *Server) listRunners(c *gin.Context) {
	user, _ := currentUser(c)
	runners, err := s.Store.Runners(c.Request.Context(), user.OrgID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	type runnerView struct {
		store.Runner
		ActiveJobs     int   `json:"active_jobs"`
		Egress24hBytes int64 `json:"egress_24h_bytes"`
	}
	egress, err := s.Store.RunnerEgress24h(c.Request.Context(), user.OrgID)
	if err != nil {
		s.Log.Warn("egress totals lookup failed", zap.Error(err))
		egress = map[uuid.UUID]int64{}
	}
	out := make([]runnerView, 0, len(runners))
	for _, r := range runners {
		jobs, err := s.Store.RunnerActiveJobs(c.Request.Context(), r.ID)
		if err != nil {
			s.Log.Warn("active jobs lookup failed", zap.String("runner", r.Name), zap.Error(err))
		}
		out = append(out, runnerView{Runner: r, ActiveJobs: len(jobs), Egress24hBytes: egress[r.ID]})
	}
	c.JSON(http.StatusOK, gin.H{"runners": out})
}

type drainRequest struct {
	Draining bool `json:"draining"`
}

func (s *Server) drainRunner(c *gin.Context) {
	user, _ := currentUser(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_id", "runner id must be a uuid")
		return
	}
	var req drainRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_body", "draining flag is required")
		return
	}
	if err := s.Store.SetRunnerDraining(c.Request.Context(), user.OrgID, id, req.Draining); err != nil {
		s.internalError(c, err)
		return
	}
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "runner.drain",
		fmt.Sprintf("%s draining=%t", id, req.Draining))
	c.JSON(http.StatusOK, gin.H{"ok": true, "draining": req.Draining})
}

// validateRunnerEnv bounds the operator env override map: sane keys, no
// control bytes, and a total size cap so a runaway map can't bloat every
// heartbeat and job env.
func validateRunnerEnv(env map[string]string) error {
	total := 0
	for k, v := range env {
		if k == "" || len(k) > 128 || strings.ContainsAny(k, "\x00=\r\n") {
			return fmt.Errorf("env key %q is invalid", k)
		}
		if strings.ContainsAny(v, "\x00\r\n") {
			return fmt.Errorf("env value for %q contains control characters", k)
		}
		total += len(k) + len(v)
	}
	if total > 16*1024 {
		return fmt.Errorf("env overrides exceed 16KB")
	}
	return nil
}

type runnerPatchRequest struct {
	Icon         *string `json:"icon"`
	WakeOnDemand *bool   `json:"wake_on_demand"`
	// Env replaces the operator-managed env override map; nil leaves it
	// unchanged, an empty object clears it.
	Env *map[string]string `json:"env"`
}

// patchRunner edits runner presentation and fleet-policy fields: the
// operator icon (an emoji or an image URL/path; "" restores the default
// marker) and wake_on_demand, which tells external scalers whether this
// runner's host may be powered on for queued work.
func (s *Server) patchRunner(c *gin.Context) {
	user, _ := currentUser(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_id", "runner id must be a uuid")
		return
	}
	var req runnerPatchRequest
	if err := c.ShouldBindJSON(&req); err != nil || (req.Icon == nil && req.WakeOnDemand == nil && req.Env == nil) {
		fail(c, http.StatusBadRequest, "invalid_body", "icon, wake_on_demand or env is required")
		return
	}
	if req.Env != nil {
		if err := validateRunnerEnv(*req.Env); err != nil {
			fail(c, http.StatusBadRequest, "invalid_body", err.Error())
			return
		}
		if err := s.Store.SetRunnerEnv(c.Request.Context(), user.OrgID, id, *req.Env); err != nil {
			s.internalError(c, err)
			return
		}
		s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "runner.env", id.String())
	}
	if req.Icon != nil {
		icon := strings.TrimSpace(*req.Icon)
		if len(icon) > 500 || strings.ContainsAny(icon, "\x00\r\n") {
			fail(c, http.StatusBadRequest, "invalid_body", "icon must be an emoji or an image URL/path (max 500 chars)")
			return
		}
		if err := s.Store.SetRunnerIcon(c.Request.Context(), user.OrgID, id, icon); err != nil {
			s.internalError(c, err)
			return
		}
		s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "runner.icon", id.String())
	}
	if req.WakeOnDemand != nil {
		if err := s.Store.SetRunnerWakeOnDemand(c.Request.Context(), user.OrgID, id, *req.WakeOnDemand); err != nil {
			s.internalError(c, err)
			return
		}
		s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "runner.wake_on_demand", id.String())
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "icon": req.Icon, "wake_on_demand": req.WakeOnDemand})
}

// autoUpdateRunner toggles the server-side self-update opt-in — the runner
// receives signed image offers on its next idle heartbeat, regardless of its
// local KERN_RUNNER_UPDATE env flag.
func (s *Server) autoUpdateRunner(c *gin.Context) {
	user, _ := currentUser(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_id", "runner id must be a uuid")
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_body", "enabled is required")
		return
	}
	if err := s.Store.SetRunnerAutoUpdate(c.Request.Context(), user.OrgID, id, req.Enabled); err != nil {
		s.internalError(c, err)
		return
	}
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "runner.auto_update", id.String())
	c.JSON(http.StatusOK, gin.H{"ok": true, "auto_update": req.Enabled})
}

func (s *Server) deleteRunner(c *gin.Context) {
	user, _ := currentUser(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_id", "runner id must be a uuid")
		return
	}
	if err := s.Store.DeleteRunner(c.Request.Context(), user.OrgID, id); err != nil {
		s.internalError(c, err)
		return
	}
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "runner.delete", id.String())
	c.Status(http.StatusNoContent)
}

type enrollRequest struct {
	Name       string `json:"name"`
	TTLMinutes int    `json:"ttl_minutes"`
	// Config is the desired runner configuration applied at registration:
	// auto_update opt-in and operator env overrides.
	Config *struct {
		AutoUpdate *bool             `json:"auto_update"`
		Env        map[string]string `json:"env"`
	} `json:"config"`
}

// createEnrollToken mints a one-time runner enrollment token. The plaintext is
// returned once and shown to the operator for `kern-runner join`; only the hash
// is stored. Registered under /enroll-tokens rather than /runners/enroll-tokens
// because gin rejects a static segment that collides with /runners/:id.
func (s *Server) createEnrollToken(c *gin.Context) {
	user, _ := currentUser(c)
	var req enrollRequest
	if err := c.ShouldBindJSON(&req); err != nil && err != io.EOF {
		fail(c, http.StatusBadRequest, "invalid_body", "invalid enroll token body")
		return
	}
	ttl := req.TTLMinutes
	if ttl <= 0 {
		ttl = 60
	}
	if ttl > 24*60 {
		ttl = 24 * 60
	}
	token, hash, err := auth.NewToken(auth.EnrollPrefix)
	if err != nil {
		s.internalError(c, err)
		return
	}
	cfg := store.RunnerEnrollConfig{}
	if req.Config != nil {
		cfg.AutoUpdate = req.Config.AutoUpdate
		cfg.Env = req.Config.Env
	}
	rec, err := s.Store.CreateEnrollToken(c.Request.Context(), user.OrgID, &user.ID,
		req.Name, hash, time.Now().Add(time.Duration(ttl)*time.Minute), cfg)
	if err != nil {
		s.internalError(c, err)
		return
	}
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "runner.enroll_token", req.Name)
	c.JSON(http.StatusCreated, gin.H{"token": token, "record": rec})
}

type jitRunnerRequest struct {
	Name   string   `json:"name"`
	Labels []string `json:"labels"`
}

// jitRunner mints a ready-to-use runner credential for external scalers
// (actions-runner-controller, custom autoscalers, spot fleets). The response
// is everything `kern-runner --token` needs; the runner is marked ephemeral
// and self-deregisters when idle. POST /api/v1/runners is the JIT endpoint —
// /runners/jit is unreachable because gin rejects a static segment beside the
// /runners/:id wildcard.
func (s *Server) jitRunner(c *gin.Context) {
	user, _ := currentUser(c)
	var req jitRunnerRequest
	if err := c.ShouldBindJSON(&req); err != nil && err != io.EOF {
		fail(c, http.StatusBadRequest, "invalid_body", "invalid JIT runner body")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "jit-" + uuid.NewString()[:8]
	}
	token, hash, err := auth.NewToken(auth.RunnerPrefix)
	if err != nil {
		s.internalError(c, err)
		return
	}
	runner, err := s.Store.InsertRunner(c.Request.Context(), store.RegisterRunnerParams{
		OrgID: user.OrgID, Name: name, Labels: req.Labels,
		Version: version.Version, TokenHash: hash, Ephemeral: true,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			fail(c, http.StatusConflict, "name_taken", "a runner with this name already exists")
			return
		}
		s.internalError(c, err)
		return
	}
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "runner.jit", runner.Name)
	c.JSON(http.StatusCreated, gin.H{
		"runner_id":    runner.ID,
		"name":         runner.Name,
		"token":        token,
		"control_url":  s.Cfg.PublicBaseURL,
		"ephemeral":    true,
		"expires_hint": "runner self-deregisters when idle; delete the row to revoke immediately",
	})
}

// runnersOnlineWindow is how long after the last heartbeat a runner is considered online.
const runnersOnlineWindow = 45 * time.Second
