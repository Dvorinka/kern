package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

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
		jobs, _ := s.Store.RunnerActiveJobs(c.Request.Context(), r.ID)
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

// runnersOnlineWindow is how long after the last heartbeat a runner is considered online.
const runnersOnlineWindow = 45 * time.Second
