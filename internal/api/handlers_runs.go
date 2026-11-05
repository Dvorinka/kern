package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/dvorinka/kern/internal/store"
)

func (s *Server) listRuns(c *gin.Context) {
	user, _ := currentUser(c)
	filter := store.RunFilter{
		Status: c.Query("status"),
		Limit:  parseInt(c.DefaultQuery("limit", "50"), 50),
		Offset: parseInt(c.DefaultQuery("offset", "0"), 0),
	}
	if slug := c.Query("project"); slug != "" {
		p, err := s.Store.Project(c.Request.Context(), user.OrgID, slug)
		if err != nil {
			s.internalError(c, err)
			return
		}
		filter.ProjectID = &p.ID
	}
	runs, total, err := s.Store.Runs(c.Request.Context(), filter)
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"runs": runs, "total": total})
}

func (s *Server) getRun(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_id", "run id must be a uuid")
		return
	}
	run, err := s.Store.Run(c.Request.Context(), id)
	if err != nil {
		s.internalError(c, err)
		return
	}
	jobs, err := s.Store.JobsForRun(c.Request.Context(), run.ID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	version, err := s.Store.WorkflowVersion(c.Request.Context(), run.WorkflowVersionID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	var plan json.RawMessage
	if len(version.Parsed) > 0 && string(version.Parsed) != "null" {
		plan = version.Parsed
	}
	deployments, err := s.Store.DeploymentsForRun(c.Request.Context(), run.ID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"run":         run,
		"jobs":        jobs,
		"plan":        plan,
		"deployments": deployments,
		"workflow": gin.H{
			"id":         run.WorkflowID,
			"path":       run.WorkflowPath,
			"name":       run.WorkflowName,
			"commit_sha": version.CommitSHA,
		},
	})
}

func (s *Server) cancelRun(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_id", "run id must be a uuid")
		return
	}
	run, err := s.Store.CancelRun(c.Request.Context(), id)
	if err != nil {
		s.internalError(c, err)
		return
	}
	user, _ := currentUser(c)
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "run.cancel", run.ID.String())
	c.JSON(http.StatusOK, gin.H{"run": run})
}

// approveRun releases a run parked in awaiting_approval (fork-PR gate).
// Rejecting is the existing cancel endpoint.
func (s *Server) approveRun(c *gin.Context) {
	user, _ := currentUser(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_id", "run id must be a uuid")
		return
	}
	if err := s.Store.ApproveRunHold(c.Request.Context(), id); err != nil {
		fail(c, http.StatusConflict, "not_awaiting", err.Error())
		return
	}
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "run.approve", id.String())
	run, err := s.Store.Run(c.Request.Context(), id)
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"run": run})
}

func (s *Server) rerunRun(c *gin.Context) {
	user, _ := currentUser(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_id", "run id must be a uuid")
		return
	}
	orig, err := s.Store.Run(c.Request.Context(), id)
	if err != nil {
		s.internalError(c, err)
		return
	}
	var source map[string]any
	_ = json.Unmarshal(orig.Source, &source)
	run, err := s.Orch.StartRun(c.Request.Context(), store.CreateRunParams{
		ProjectID:         orig.ProjectID,
		WorkflowID:        orig.WorkflowID,
		WorkflowVersionID: orig.WorkflowVersionID,
		Event:             orig.Event,
		Ref:               orig.Ref,
		SHA:               orig.SHA,
		CommitMessage:     orig.CommitMessage,
		Actor:             user.Email,
		TrustLevel:        orig.TrustLevel,
		Source:            source,
		ConcurrencyGroup:  orig.ConcurrencyGroup,
	})
	if err != nil {
		s.internalError(c, err)
		return
	}
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "run.rerun", orig.ID.String())
	c.JSON(http.StatusCreated, gin.H{"run": run})
}

func (s *Server) stepLogs(c *gin.Context) {
	step, _, jobID, err := s.StepByIDParam(c)
	if err != nil {
		return
	}
	offset := int64(parseInt(c.DefaultQuery("offset", "0"), 0))
	max := parseInt(c.DefaultQuery("limit", "262144"), 262144)
	if step.LogRef == "" {
		// Steps skipped server-side never ran a runner, so nothing was
		// posted. Surface the recorded reason instead of an empty log.
		text := ""
		if step.Conclusion == "skipped" || step.Conclusion == "cancelled" {
			text = "KERN: step " + step.Conclusion
			if job, jerr := s.Store.Job(c.Request.Context(), jobID); jerr == nil && job.Error != "" {
				text += " — " + job.Error
			}
			text += "\n"
		}
		c.JSON(http.StatusOK, gin.H{"text": text, "next_offset": 0, "size": int64(len(text)),
			"status": step.Status, "conclusion": step.Conclusion})
		return
	}
	data, size, err := s.Blobs.ReadFrom(step.LogRef, offset, max)
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"text":        string(data),
		"next_offset": offset + int64(len(data)),
		"size":        size,
		"status":      step.Status,
		"conclusion":  step.Conclusion,
	})
}

// stepSummary serves a step's stored GITHUB_STEP_SUMMARY markdown.
func (s *Server) stepSummary(c *gin.Context) {
	step, _, _, err := s.StepByIDParam(c)
	if err != nil {
		return
	}
	if step.SummaryRef == "" {
		c.JSON(http.StatusOK, gin.H{"text": ""})
		return
	}
	data, _, err := s.Blobs.ReadFrom(step.SummaryRef, 0, 1<<20)
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"text": string(data)})
}

// stepLogStream streams a running step's log over Server-Sent Events.
func (s *Server) stepLogStream(c *gin.Context) {
	step, _, _, err := s.StepByIDParam(c)
	if err != nil {
		return
	}
	follow := c.Query("follow") != "0"

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	offset := int64(parseInt(c.DefaultQuery("offset", "0"), 0))
	flusher, _ := c.Writer.(http.Flusher)
	send := func(event string, payload any) bool {
		b, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		c.Writer.WriteString("event: " + event + "\ndata: ")
		c.Writer.Write(b)
		c.Writer.WriteString("\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		return true
	}

	deadline := time.Now().Add(2 * time.Hour)
	for {
		if step.LogRef != "" {
			data, size, err := s.Blobs.ReadFrom(step.LogRef, offset, 64<<10)
			if err != nil {
				send("error", gin.H{"message": "log read failed"})
				return
			}
			if len(data) > 0 {
				if !send("log", gin.H{"offset": offset, "text": string(data)}) {
					return
				}
				offset += int64(len(data))
				if offset < size {
					continue
				}
			}
		}
		// Refresh status; the step may have finished while tailing.
		if refreshed, _, _, err := s.Store.StepByID(c.Request.Context(), step.ID); err == nil {
			step = refreshed
		}
		if step.Status != "running" && step.Status != "queued" {
			send("end", gin.H{"offset": offset, "status": step.Status, "conclusion": step.Conclusion})
			return
		}
		if !follow || time.Now().After(deadline) {
			send("end", gin.H{"offset": offset, "status": step.Status, "conclusion": step.Conclusion})
			return
		}
		select {
		case <-c.Request.Context().Done():
			return
		case <-time.After(400 * time.Millisecond):
		}
	}
}

// StepByIDParam resolves the step id path parameter, writing an error response when invalid.
func (s *Server) StepByIDParam(c *gin.Context) (store.Step, uuid.UUID, uuid.UUID, error) {
	id, err := uuid.Parse(c.Param("stepID"))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_id", "step id must be a uuid")
		return store.Step{}, uuid.Nil, uuid.Nil, err
	}
	step, runID, jobID, err := s.Store.StepByID(c.Request.Context(), id)
	if err != nil {
		s.internalError(c, err)
		return store.Step{}, uuid.Nil, uuid.Nil, err
	}
	return step, runID, jobID, nil
}

func parseInt(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}
