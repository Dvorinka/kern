package api

import (
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/dvorinka/kern/internal/auth"
	"github.com/dvorinka/kern/internal/store"
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
		ActiveJobs int `json:"active_jobs"`
	}
	out := make([]runnerView, 0, len(runners))
	for _, r := range runners {
		jobs, err := s.Store.RunnerActiveJobs(c.Request.Context(), r.ID)
		if err != nil {
			s.Log.Warn("active jobs lookup failed", zap.String("runner", r.Name), zap.Error(err))
		}
		out = append(out, runnerView{Runner: r, ActiveJobs: len(jobs)})
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
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "runner.drain", id.String())
	c.JSON(http.StatusOK, gin.H{"ok": true, "draining": req.Draining})
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
	rec, err := s.Store.CreateEnrollToken(c.Request.Context(), user.OrgID, &user.ID,
		req.Name, hash, time.Now().Add(time.Duration(ttl)*time.Minute))
	if err != nil {
		s.internalError(c, err)
		return
	}
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "runner.enroll_token", req.Name)
	c.JSON(http.StatusCreated, gin.H{"token": token, "record": rec})
}

// runnersOnlineWindow is how long after the last heartbeat a runner is considered online.
const runnersOnlineWindow = 45 * time.Second
