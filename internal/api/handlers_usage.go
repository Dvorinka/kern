package api

import (
	"bufio"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/dvorinka/kern/internal/store"
)

// githubLinuxMinuteUSD is GitHub's standard Linux 2-core x64 hosted-runner
// rate for private repos. The caption names the SKU so the comparison stays
// honest — larger runners bill more.
const githubLinuxMinuteUSD = 0.006

func (s *Server) usage(c *gin.Context) {
	user, _ := currentUser(c)
	days, _ := strconv.Atoi(c.DefaultQuery("days", "30"))
	ctx, cancel := contextWithTimeout(c, 8*time.Second)
	defer cancel()
	rep, err := s.Store.Usage(ctx, user.OrgID, days)
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"usage":            rep,
		"days":             days,
		"rate_usd_per_min": githubLinuxMinuteUSD,
		"hosted_equiv_usd": float64(rep.Totals.BillableMinutes) * githubLinuxMinuteUSD,
	})
}

// notifications is a derived attention feed — no table, no read state. Rows
// vanish the moment their cause is resolved (approve, rerun, heartbeat).
func (s *Server) notifications(c *gin.Context) {
	user, _ := currentUser(c)
	ctx, cancel := contextWithTimeout(c, 5*time.Second)
	defer cancel()

	out := []store.Notification{}
	approval, err := s.Store.AwaitingApprovalRuns(ctx, user.OrgID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	out = append(out, approval...)
	rem, err := s.Store.PendingRemediations(ctx, user.OrgID, 20)
	if err != nil {
		s.internalError(c, err)
		return
	}
	out = append(out, rem...)
	failed, err := s.Store.RecentFailedRuns(ctx, user.OrgID, 48*time.Hour, 10)
	if err != nil {
		s.internalError(c, err)
		return
	}
	out = append(out, failed...)
	whf, err := s.Store.WebhookFailures(ctx, 24*time.Hour, 10)
	if err != nil {
		s.internalError(c, err)
		return
	}
	out = append(out, whf...)
	offline, err := s.Store.OfflineRunners(ctx, user.OrgID, 24*time.Hour)
	if err != nil {
		s.internalError(c, err)
		return
	}
	out = append(out, offline...)

	c.JSON(http.StatusOK, gin.H{"notifications": out, "count": len(out)})
}

func (s *Server) listAudit(c *gin.Context) {
	user, _ := currentUser(c)
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	events, err := s.Store.ListAuditEvents(c.Request.Context(), user.OrgID, limit)
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"events": events})
}

// auditTimeParam parses an optional RFC3339 or YYYY-MM-DD bound.
func auditTimeParam(c *gin.Context, name string) (*time.Time, bool) {
	raw := c.Query(name)
	if raw == "" {
		return nil, true
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return &t, true
		}
	}
	fail(c, http.StatusBadRequest, "invalid_time", name+" must be RFC3339 or YYYY-MM-DD")
	return nil, false
}

// exportAudit streams the org audit trail as JSONL for compliance pulls.
// ?from=&to= bound the range (open when absent); rows go oldest-first.
func (s *Server) exportAudit(c *gin.Context) {
	user, _ := currentUser(c)
	from, ok := auditTimeParam(c, "from")
	if !ok {
		return
	}
	to, ok := auditTimeParam(c, "to")
	if !ok {
		return
	}
	if from != nil && to != nil && !from.Before(*to) {
		fail(c, http.StatusBadRequest, "invalid_range", "from must be before to")
		return
	}
	c.Header("Content-Type", "application/x-ndjson")
	c.Header("Content-Disposition", `attachment; filename="kern-audit.jsonl"`)
	w := bufio.NewWriter(c.Writer)
	defer w.Flush()
	err := s.Store.EachAuditEvent(c.Request.Context(), user.OrgID, from, to, func(e store.AuditEvent) error {
		line, err := json.Marshal(e)
		if err != nil {
			return err
		}
		_, err = w.Write(append(line, '\n'))
		return err
	})
	if err != nil {
		s.Log.Error("audit export", zap.Error(err))
	}
}
