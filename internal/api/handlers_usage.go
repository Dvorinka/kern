package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

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
