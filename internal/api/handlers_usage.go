package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dvorinka/kern/internal/store"
	"github.com/dvorinka/kern/internal/version"
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

// ── update check ─────────────────────────────────────────────────────────
// One upstream call per hour, cached in-process. The check is a convenience
// surface only — failures degrade to ok:false, never to a banner that lies.

type updateInfo struct {
	OK          bool      `json:"ok"`
	Current     string    `json:"current"`
	Latest      string    `json:"latest"`
	URL         string    `json:"url"`
	PublishedAt time.Time `json:"published_at,omitempty"`
	Available   bool      `json:"available"`
	CheckedAt   time.Time `json:"checked_at"`
	Repo        string    `json:"repo"`
	Err         string    `json:"error,omitempty"`
}

var updateCache struct {
	mu    sync.Mutex
	info  updateInfo
	until time.Time
}

func (s *Server) updateCheck(c *gin.Context) {
	repo := s.Cfg.ReleaseRepo
	if repo == "" {
		repo = "Dvorinka/kern"
	}
	force := c.Query("refresh") == "1"

	updateCache.mu.Lock()
	defer updateCache.mu.Unlock()
	if !force && time.Now().Before(updateCache.until) && updateCache.info.Repo == repo {
		c.JSON(http.StatusOK, updateCache.info)
		return
	}

	info := updateInfo{Current: version.Version, Repo: repo, CheckedAt: time.Now()}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 6*time.Second)
	defer cancel()
	latest, url, published, err := s.latestRelease(ctx, repo)
	if err != nil {
		info.Err = err.Error()
	} else {
		info.OK = true
		info.Latest = latest
		info.URL = url
		info.PublishedAt = published
		info.Available = newerThan(info.Current, latest)
	}
	updateCache.info = info
	updateCache.until = time.Now().Add(time.Hour)
	c.JSON(http.StatusOK, info)
}

func (s *Server) latestRelease(ctx context.Context, repo string) (tag, url string, published time.Time, err error) {
	endpoint := fmt.Sprintf("%s/repos/%s/releases/latest", strings.TrimRight(s.Cfg.GitHub.APIBaseURL, "/"), repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", "", published, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "kern/"+version.Version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", published, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", published, fmt.Errorf("github api %d", resp.StatusCode)
	}
	var body struct {
		TagName     string    `json:"tag_name"`
		HTMLURL     string    `json:"html_url"`
		PublishedAt time.Time `json:"published_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", "", published, err
	}
	return body.TagName, body.HTMLURL, body.PublishedAt, nil
}

// newerThan compares dotted numeric versions; anything unparseable
// ("dev", "v0.2.0-5-gdeadbeef") conservatively reports no update.
func newerThan(current, latest string) bool {
	parse := func(v string) ([3]int, bool) {
		var out [3]int
		v = strings.TrimPrefix(strings.TrimSpace(v), "v")
		parts := strings.Split(v, ".")
		if len(parts) != 3 {
			return out, false
		}
		for i, p := range parts {
			n, err := strconv.Atoi(strings.TrimRight(p, "+-"))
			if err != nil {
				// tolerate prerelease suffixes like "0-rc1" by splitting on '-'
				n, err = strconv.Atoi(strings.SplitN(p, "-", 2)[0])
				if err != nil {
					return out, false
				}
			}
			out[i] = n
		}
		return out, true
	}
	cur, ok1 := parse(current)
	lat, ok2 := parse(latest)
	if !ok1 || !ok2 {
		return false
	}
	for i := range cur {
		if lat[i] != cur[i] {
			return lat[i] > cur[i]
		}
	}
	return false
}
