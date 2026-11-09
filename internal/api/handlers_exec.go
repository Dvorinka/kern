package api

import (
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/dvorinka/kern/internal/blobs"
	"github.com/dvorinka/kern/internal/store"
)

// createExec queues a live-exec session against an in_progress job (#132).
// Admin role: the session runs inside the job container with the job's
// full environment, so it is a write-capable action even for read-only
// commands. Fork/pull_request runs never get a shell — untrusted code
// must not invite the operator into its own sandbox.
func (s *Server) createExec(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_id", "job id must be a uuid")
		return
	}
	var req struct {
		Command     string `json:"command"`
		Interactive bool   `json:"interactive"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_body", "invalid exec body")
		return
	}
	if !req.Interactive && strings.TrimSpace(req.Command) == "" {
		fail(c, http.StatusBadRequest, "invalid_body", "command is required for a non-interactive exec")
		return
	}
	job, err := s.Store.Job(c.Request.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		fail(c, http.StatusNotFound, "not_found", "job not found")
		return
	}
	if err != nil {
		s.internalError(c, err)
		return
	}
	if job.Status != "in_progress" || job.RunnerID == nil {
		fail(c, http.StatusConflict, "job_not_running", "job is not running on a runner")
		return
	}
	run, err := s.Store.Run(c.Request.Context(), job.RunID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	if run.TrustLevel == "fork" || run.TrustLevel == "pull_request" {
		fail(c, http.StatusForbidden, "untrusted_run", "exec is disabled for untrusted runs")
		return
	}
	runner, err := s.Store.Runner(c.Request.Context(), *job.RunnerID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	if runner.Executor != "" && runner.Executor != "docker" {
		fail(c, http.StatusConflict, "host_executor", "exec needs the docker executor; this job runs on a host executor")
		return
	}
	user, _ := currentUser(c)
	createdBy := &user.ID
	ex, err := s.Store.CreateExec(c.Request.Context(), job.ID, req.Command, req.Interactive, createdBy)
	if err != nil {
		s.internalError(c, err)
		return
	}
	s.Store.Audit(c.Request.Context(), user.OrgID, createdBy, "exec.create", job.ID.String())
	c.JSON(http.StatusCreated, gin.H{"exec": ex})
}

// execForUser resolves an exec request for the user-facing routes.
func (s *Server) execForUser(c *gin.Context) (store.ExecRequest, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_id", "exec id must be a uuid")
		return store.ExecRequest{}, false
	}
	ex, err := s.Store.Exec(c.Request.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		fail(c, http.StatusNotFound, "not_found", "exec session not found")
		return ex, false
	}
	if err != nil {
		s.internalError(c, err)
		return ex, false
	}
	return ex, true
}

// getExec reports session state and, with ?after=<offset>, the next output
// chunk. Clients poll this like step-log streaming.
func (s *Server) getExec(c *gin.Context) {
	ex, ok := s.execForUser(c)
	if !ok {
		return
	}
	resp := gin.H{"exec": ex}
	after := int64(parseInt(c.DefaultQuery("after", "-1"), -1))
	if after >= 0 {
		chunk, size, err := s.Blobs.ReadFrom(blobs.ExecOutKey(ex.JobID.String(), ex.ID.String()), after, 256<<10)
		if err != nil {
			s.internalError(c, err)
			return
		}
		resp["data"] = base64.StdEncoding.EncodeToString(chunk)
		resp["size"] = size
	}
	c.JSON(http.StatusOK, resp)
}

// postExecStdin appends a chunk to the session's stdin pipe; ?eof=1 closes
// it. Same trust gate as creation — stdin is write capability.
func (s *Server) postExecStdin(c *gin.Context) {
	ex, ok := s.execForUser(c)
	if !ok {
		return
	}
	if ex.State != "pending" && ex.State != "running" {
		fail(c, http.StatusConflict, "exec_finished", "exec session is finished")
		return
	}
	if c.Query("eof") == "1" {
		if err := s.Store.CloseExecStdin(c.Request.Context(), ex.ID); err != nil {
			s.internalError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 64<<10))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_body", "stdin chunk too large or unreadable")
		return
	}
	if _, err := s.Blobs.Append(blobs.ExecStdinKey(ex.JobID.String(), ex.ID.String()), body); err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// runnerExec resolves an exec request for the runner-facing routes and
// verifies the exec's job is still leased to this runner.
func (s *Server) runnerExec(c *gin.Context) (store.ExecRequest, store.Job, store.Runner, bool) {
	runner, ok := currentRunner(c)
	if !ok {
		fail(c, http.StatusUnauthorized, "unauthenticated", "runner authentication required")
		return store.ExecRequest{}, store.Job{}, runner, false
	}
	id, err := uuid.Parse(c.Param("execID"))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_id", "exec id must be a uuid")
		return store.ExecRequest{}, store.Job{}, runner, false
	}
	ex, err := s.Store.Exec(c.Request.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		fail(c, http.StatusNotFound, "not_found", "exec session not found")
		return ex, store.Job{}, runner, false
	}
	if err != nil {
		s.internalError(c, err)
		return ex, store.Job{}, runner, false
	}
	job, err := s.Store.Job(c.Request.Context(), ex.JobID)
	if err != nil {
		s.internalError(c, err)
		return ex, job, runner, false
	}
	if job.RunnerID == nil || *job.RunnerID != runner.ID {
		fail(c, http.StatusNotFound, "not_found", "job is not leased to this runner")
		return ex, job, runner, false
	}
	return ex, job, runner, true
}

// execStart marks a session running under this runner's lease.
func (s *Server) execStart(c *gin.Context) {
	ex, _, runner, ok := s.runnerExec(c)
	if !ok {
		return
	}
	started, err := s.Store.StartExec(c.Request.Context(), ex.ID, runner.ID)
	if errors.Is(err, store.ErrNotFound) {
		fail(c, http.StatusConflict, "stale_exec", "exec request is no longer pending")
		return
	}
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"exec": started})
}

// execOutput appends a masked output chunk to the session blob.
func (s *Server) execOutput(c *gin.Context) {
	ex, job, _, ok := s.runnerExec(c)
	if !ok {
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_body", "output chunk too large or unreadable")
		return
	}
	data := s.maskerForJob(c, job).MaskBytes(body)
	size, err := s.Blobs.Append(blobs.ExecOutKey(job.ID.String(), ex.ID.String()), data)
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "size": size})
}

// execStdinFeed returns stdin bytes after an offset for the runner's poll.
func (s *Server) execStdinFeed(c *gin.Context) {
	ex, _, _, ok := s.runnerExec(c)
	if !ok {
		return
	}
	after := int64(parseInt(c.DefaultQuery("after", "0"), 0))
	chunk, size, err := s.Blobs.ReadFrom(blobs.ExecStdinKey(ex.JobID.String(), ex.ID.String()), after, 64<<10)
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"data":   base64.StdEncoding.EncodeToString(chunk),
		"size":   size,
		"closed": ex.StdinClosed,
	})
}

// execFinish records the exit state for a session.
func (s *Server) execFinish(c *gin.Context) {
	ex, _, runner, ok := s.runnerExec(c)
	if !ok {
		return
	}
	var req struct {
		ExitCode *int   `json:"exit_code"`
		Error    string `json:"error"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_body", "invalid finish body")
		return
	}
	if err := s.Store.FinishExec(c.Request.Context(), ex.ID, runner.ID, req.ExitCode, req.Error); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail(c, http.StatusConflict, "stale_exec", "exec session is no longer running")
			return
		}
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
