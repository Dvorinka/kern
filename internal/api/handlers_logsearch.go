package api

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Bounds for the log-search endpoint (#135). The scan runs newest-first, so
// a stopped scan loses old history, never recent. No index exists — the
// endpoint greps blob chunks server-side, so the caps are what keep a
// full-retention corpus from pinning a request.
const (
	logSearchMaxSteps   = 2000             // candidate step logs scanned
	logSearchMaxBytes   = 256 << 20        // total log bytes read
	logSearchMaxHits    = 200              // result cap hard ceiling
	logSearchScanBuf    = 256 << 10        // bufio.Scanner line buffer
	logSearchTextMax    = 300              // per-hit text truncation
	logSearchDefaultHit = 50               // hits when limit is unset
	logSearchDeadline   = 25 * time.Second // whole-request ceiling
)

type logSearchHit struct {
	RunID      string `json:"run_id"`
	RunNumber  int    `json:"run_number"`
	Project    string `json:"project"`
	Workflow   string `json:"workflow"`
	JobKey     string `json:"job_key"`
	JobName    string `json:"job_name"`
	StepNumber int    `json:"step_number"`
	StepName   string `json:"step_name"`
	Line       int    `json:"line"`
	Text       string `json:"text"`
}

// searchLogs greps step-log blobs across the org. GET /logs/search takes
// q (required, case-insensitive substring), optional project/run/limit,
// and returns hits newest-first plus counters that show whether the scan
// was truncated by a bound rather than by the end of the corpus.
func (s *Server) searchLogs(c *gin.Context) {
	user, _ := currentUser(c)
	q := strings.TrimSpace(c.Query("q"))
	if q == "" {
		fail(c, http.StatusBadRequest, "invalid_query", "q is required")
		return
	}
	limit := parseInt(c.DefaultQuery("limit", ""), logSearchDefaultHit)
	if limit <= 0 || limit > logSearchMaxHits {
		limit = logSearchMaxHits
	}
	var runID uuid.UUID
	if v := c.Query("run"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			runID = id
		} else {
			// CLI users pass the short printed id; accept an unambiguous prefix.
			cands, cerr := s.Store.RunPrefix(c.Request.Context(), v)
			if cerr != nil || len(cands) != 1 {
				fail(c, http.StatusBadRequest, "invalid_run", "run must be a uuid or unambiguous prefix")
				return
			}
			runID = cands[0].ID
		}
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), logSearchDeadline)
	defer cancel()
	steps, err := s.Store.SearchLogSteps(ctx, user.OrgID, c.Query("project"), runID, logSearchMaxSteps)
	if err != nil {
		s.internalError(c, err)
		return
	}
	needle := strings.ToLower(q)
	hits := make([]logSearchHit, 0, limit)
	var scanned int64
	stepsSeen := 0
	truncated := false
	for _, st := range steps {
		if len(hits) >= limit || scanned >= logSearchMaxBytes || ctx.Err() != nil {
			truncated = true
			break
		}
		rc, _, err := s.Blobs.Open(st.LogRef)
		if err != nil || rc == nil {
			continue
		}
		stepsSeen++
		sc := bufio.NewScanner(rc)
		sc.Buffer(make([]byte, 0, 64<<10), logSearchScanBuf)
		line := 0
		for sc.Scan() {
			line++
			b := sc.Bytes()
			scanned += int64(len(b))
			if !strings.Contains(strings.ToLower(string(b)), needle) {
				continue
			}
			text := sc.Text()
			if len(text) > logSearchTextMax {
				text = text[:logSearchTextMax] + "…"
			}
			hits = append(hits, logSearchHit{
				RunID: st.RunID.String(), RunNumber: st.RunNumber,
				Project: st.Project, Workflow: st.Workflow,
				JobKey: st.JobKey, JobName: st.JobName,
				StepNumber: st.Number, StepName: st.Name,
				Line: line, Text: text,
			})
			if len(hits) >= limit {
				break
			}
		}
		_ = rc.Close()
		if scanned >= logSearchMaxBytes {
			truncated = true
			break
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"hits": hits, "steps_scanned": stepsSeen, "bytes_scanned": scanned,
		"truncated": truncated,
	})
}
