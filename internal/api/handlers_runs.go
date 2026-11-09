package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/dvorinka/kern/internal/store"
)

func (s *Server) listRuns(c *gin.Context) {
	user, _ := currentUser(c)
	filter := store.RunFilter{
		Status:         c.Query("status"),
		Conclusion:     c.Query("conclusion"),
		IncludeCleared: c.Query("include_cleared") == "1" || c.Query("include_cleared") == "true",
		Ref:            c.Query("ref"),
		PR:             c.Query("pr"),
		Workflow:       c.Query("workflow"),
		Limit:          parseInt(c.DefaultQuery("limit", "50"), 50),
		Offset:         parseInt(c.DefaultQuery("offset", "0"), 0),
	}
	// ?day=<RFC3339> scopes to the 24h window starting at that instant —
	// the overview chart's bar drill-down. The client sends local midnight.
	if day := c.Query("day"); day != "" {
		if t, err := time.Parse(time.RFC3339, day); err == nil {
			filter.Since = t
			filter.Until = t.Add(24 * time.Hour)
		}
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
	counts, err := s.Store.RunCounts(c.Request.Context(), filter.ProjectID, filter.IncludeCleared)
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"runs": runs, "total": total, "counts": counts})
}

// dayCounts serves the overview charts — per-day per-project totals from
// the table itself, so a 14-day or 10-week view does not depend on how
// many runs the paged list happened to return.
func (s *Server) dayCounts(c *gin.Context) {
	user, _ := currentUser(c)
	var projectID *uuid.UUID
	if slug := c.Query("project"); slug != "" {
		p, err := s.Store.Project(c.Request.Context(), user.OrgID, slug)
		if err != nil {
			s.internalError(c, err)
			return
		}
		projectID = &p.ID
	}
	days, err := s.Store.RunDayCounts(c.Request.Context(), projectID,
		parseInt(c.DefaultQuery("days", "30"), 30))
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"days": days})
}

// queuedJobs lists the real queue — jobs parked waiting for a runner,
// in claim order (priority then age). The overview and runs pages used
// to fan out a run-detail read per live run to reconstruct this.
func (s *Server) queuedJobs(c *gin.Context) {
	jobs, err := s.Store.QueuedJobs(c.Request.Context(),
		parseInt(c.DefaultQuery("limit", "100"), 100))
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"jobs": jobs})
}

// clearCancelledRuns hides every cleared-eligible cancelled run from the
// default lists. Rows and history are kept; pass ?project=<slug> to scope.
func (s *Server) clearCancelledRuns(c *gin.Context) {
	user, _ := currentUser(c)
	var projectID *uuid.UUID
	if slug := c.Query("project"); slug != "" {
		p, err := s.Store.Project(c.Request.Context(), user.OrgID, slug)
		if err != nil {
			s.internalError(c, err)
			return
		}
		projectID = &p.ID
	}
	n, err := s.Store.ClearCancelledRuns(c.Request.Context(), user.OrgID, projectID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "runs.clear_cancelled", "")
	c.JSON(http.StatusOK, gin.H{"cleared": n})
}

// runIDParam resolves the :id route param — a full UUID or an unambiguous
// hex prefix (the short ids `kern runs list` prints). Writes the error
// response and returns false when the id is invalid, unknown, or ambiguous.
func (s *Server) runIDParam(c *gin.Context) (uuid.UUID, bool) {
	raw := strings.ToLower(c.Param("id"))
	if id, err := uuid.Parse(raw); err == nil {
		return id, true
	}
	if len(raw) < 4 || len(raw) > 32 {
		fail(c, http.StatusBadRequest, "invalid_id", "run id must be a uuid or an 8+ char prefix")
		return uuid.Nil, false
	}
	for _, r := range raw {
		if !strings.ContainsRune("0123456789abcdef", r) {
			fail(c, http.StatusBadRequest, "invalid_id", "run id must be a uuid or an 8+ char prefix")
			return uuid.Nil, false
		}
	}
	cands, err := s.Store.RunPrefix(c.Request.Context(), raw)
	if err != nil {
		s.internalError(c, err)
		return uuid.Nil, false
	}
	switch len(cands) {
	case 0:
		fail(c, http.StatusNotFound, "not_found", "run not found")
	case 1:
		return cands[0].ID, true
	default:
		ids := make([]string, 0, len(cands))
		for _, r := range cands {
			ids = append(ids, r.ID.String()[:8])
		}
		fail(c, http.StatusBadRequest, "ambiguous_id",
			"prefix matches multiple runs: "+strings.Join(ids, ", ")+" — use a longer prefix")
	}
	return uuid.Nil, false
}

func (s *Server) getRun(c *gin.Context) {
	id, ok := s.runIDParam(c)
	if !ok {
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
	// Advisory only: a diagnostic failure must not take the run page down.
	if unmatched, err := s.Store.UnmatchedQueuedJobs(c.Request.Context()); err == nil {
		starved := make(map[uuid.UUID]bool, len(unmatched))
		for _, u := range unmatched {
			if u.RunID == run.ID {
				starved[u.JobID] = true
			}
		}
		for i := range jobs {
			if starved[jobs[i].ID] {
				jobs[i].QueueReason = "no online runner matches runs-on labels"
			}
		}
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
	id, ok := s.runIDParam(c)
	if !ok {
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
	id, ok := s.runIDParam(c)
	if !ok {
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
	id, ok := s.runIDParam(c)
	if !ok {
		return
	}
	orig, err := s.Store.Run(c.Request.Context(), id)
	if err != nil {
		s.internalError(c, err)
		return
	}
	// Optional body switches to an in-place rerun of the finished run's jobs
	// (gh rerun --failed / --job) instead of seeding a fresh run.
	var req struct {
		Failed bool     `json:"failed"`
		Jobs   []string `json:"jobs"`
	}
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		fail(c, http.StatusBadRequest, "bad_body", "invalid JSON body")
		return
	}
	if req.Failed || len(req.Jobs) > 0 {
		n, err := s.Store.RerunJobs(c.Request.Context(), orig.ID, req.Jobs)
		if err != nil {
			fail(c, http.StatusConflict, "rerun_failed", err.Error())
			return
		}
		s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "run.rerun_jobs", orig.ID.String())
		run, err := s.Store.Run(c.Request.Context(), orig.ID)
		if err != nil {
			s.internalError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"run": run, "jobs": n})
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
	// Re-triggering a cancelled run retires the cancelled entry from the
	// default lists; the row and its history stay reachable by id.
	if orig.Status == "cancelled" || orig.Conclusion == "cancelled" {
		_, _ = s.Store.ClearRun(c.Request.Context(), orig.ID)
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
