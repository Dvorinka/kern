package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/dvorinka/kern/internal/store"
	"github.com/dvorinka/kern/internal/workflows"
)

const (
	ctxTokenID       = "kern.token_id"
	ctxTokenProjects = "kern.token_projects"
	ctxTokenLabels   = "kern.token_labels"
	ctxTokenCap      = "kern.token_cap"
)

// Token grants limit an API token to projects and a runner-label pool.
// Empty lists are unrestricted (sessions, and tokens minted without them).

func tokenProjects(c *gin.Context) []string {
	list, _ := c.Get(ctxTokenProjects)
	out, _ := list.([]string)
	return out
}

func tokenLabels(c *gin.Context) []string {
	list, _ := c.Get(ctxTokenLabels)
	out, _ := list.([]string)
	return out
}

func tokenID(c *gin.Context) (uuid.UUID, bool) {
	v, ok := c.Get(ctxTokenID)
	if !ok {
		return uuid.Nil, false
	}
	id, ok := v.(uuid.UUID)
	return id, ok && id != uuid.Nil
}

// dispatchTokenID stamps the run with the token that started it so the
// claim query can hold the token to its max_concurrent_jobs budget.
func dispatchTokenID(c *gin.Context) uuid.UUID {
	id, _ := tokenID(c)
	return id
}

// tokenCap is the dispatching token's max_concurrent_jobs; 0 is uncapped
// (sessions, and tokens minted without a cap).
func tokenCap(c *gin.Context) int {
	v, _ := c.Get(ctxTokenCap)
	n, _ := v.(int)
	return n
}

func grantRestricted(c *gin.Context) bool {
	return len(tokenProjects(c)) > 0 || len(tokenLabels(c)) > 0
}

func grantHas(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

// cleanGrantList trims, dedupes and rejects values that cannot be a project
// slug or a runner label. lower folds project slugs; labels keep their case.
func cleanGrantList(kind string, in []string, lower bool) ([]string, error) {
	if len(in) > 32 {
		return nil, fmt.Errorf("too many %ss (max 32)", kind)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		v := strings.TrimSpace(raw)
		if lower {
			v = strings.ToLower(v)
		}
		if v == "" {
			continue
		}
		if strings.ContainsAny(v, " \t,/\\") || len(v) > 64 {
			return nil, fmt.Errorf("invalid %s %q", kind, raw)
		}
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out, nil
}

// narrowList refuses a child grant wider than its parent. An empty parent
// is unrestricted and may mint anything, including an empty child.
func narrowList(kind string, parent, child []string) error {
	if len(parent) == 0 {
		return nil
	}
	if len(child) == 0 {
		return fmt.Errorf("a %s-scoped token cannot mint an unrestricted token", kind)
	}
	for _, c := range child {
		if !grantHas(parent, c) {
			return fmt.Errorf("%s %q is outside this token's grant", kind, c)
		}
	}
	return nil
}

func maxScope(scopes []string) string {
	best := "read"
	for _, sc := range scopes {
		if scopeRank(sc) > scopeRank(best) {
			best = sc
		}
	}
	return best
}

// runsOnDenial reports why a pool token cannot dispatch these jobs.
// An empty allowlist is unrestricted. Every requested label must be in the
// allowlist, and a job with no runs-on is refused: it would match any runner.
func runsOnDenial(allow []string, jobs []workflows.JobPlan) string {
	if len(allow) == 0 {
		return ""
	}
	if len(jobs) == 0 {
		return "this token is limited to a runner pool and the workflow has no jobs"
	}
	for _, j := range jobs {
		name := j.Key
		if name == "" {
			name = j.Name
		}
		if len(j.RunsOn) == 0 {
			return "job " + name + " has no runs-on; a pool token cannot dispatch it"
		}
		for _, label := range j.RunsOn {
			if !grantHas(allow, label) {
				return fmt.Sprintf("job %s requests label %q outside this token's pool", name, label)
			}
		}
	}
	return ""
}

// grantRouteAllowed is the surface a project-scoped token may touch.
// Everything else (secrets, runners, MCP, org settings) stays on a
// session or an unrestricted token.
func grantRouteAllowed(path string) bool {
	switch path {
	case "/api/v1/auth/me", "/api/v1/auth/logout",
		"/api/v1/auth/tokens", "/api/v1/auth/tokens/:id",
		"/api/v1/projects",
		"/api/v1/runs", "/api/v1/runs/:id", "/api/v1/runs/:id/cancel", "/api/v1/runs/:id/rerun",
		"/api/v1/artifacts", "/api/v1/artifacts/:id", "/api/v1/artifacts/:id/download",
		"/api/v1/steps/:stepID/logs", "/api/v1/steps/:stepID/logs/stream", "/api/v1/steps/:stepID/summary",
		"/api/v1/mcp", "/api/v1/logs/search":
		return true
	default:
		return strings.HasPrefix(path, "/api/v1/projects/:project")
	}
}

// constrainGrant keeps a project-scoped token inside its projects.
// Label pools are checked at dispatch, where the workflow plan exists.
func (s *Server) constrainGrant() gin.HandlerFunc {
	return func(c *gin.Context) {
		projects := tokenProjects(c)
		if c.GetString(ctxAuthKind) != "api_token" || len(projects) == 0 {
			c.Next()
			return
		}
		path := c.FullPath()
		if path == "" || !grantRouteAllowed(path) {
			fail(c, http.StatusForbidden, "token_scoped", "this token is limited to specific projects")
			return
		}
		if p := c.Param("project"); p != "" && !grantHas(projects, p) {
			fail(c, http.StatusForbidden, "project_denied", "token cannot access project "+p)
			return
		}
		if !s.enforceGrantIDs(c, path) {
			return
		}
		c.Next()
	}
}

// enforceGrantIDs checks run, artifact and step ids on the allowed routes.
// It returns false when it has already answered the request.
func (s *Server) enforceGrantIDs(c *gin.Context, path string) bool {
	switch path {
	case "/api/v1/runs":
		slug := c.Query("project")
		if slug == "" {
			fail(c, http.StatusForbidden, "project_required", "this token must pass ?project= set to one of its projects")
			return false
		}
		if !grantHas(tokenProjects(c), slug) {
			fail(c, http.StatusForbidden, "project_denied", "token cannot access project "+slug)
			return false
		}
		return true
	case "/api/v1/artifacts":
		raw := c.Query("run_id")
		if raw == "" {
			fail(c, http.StatusForbidden, "run_required", "this token must pass run_id")
			return false
		}
		id, err := uuid.Parse(raw)
		if err != nil {
			return true
		}
		return s.enforceRunGrant(c, id)
	case "/api/v1/runs/:id", "/api/v1/runs/:id/cancel", "/api/v1/runs/:id/rerun":
		// Full UUIDs and short prefixes. A prefix must be resolved here:
		// falling through lets the handler serve another project's run.
		id, ok := s.runIDParam(c)
		if !ok {
			return false
		}
		return s.enforceRunGrant(c, id)
	case "/api/v1/logs/search":
		// Scoped tokens must pin the search to one of their projects —
		// omitting it would search every project's logs.
		slug := c.Query("project")
		if slug == "" {
			fail(c, http.StatusForbidden, "project_required", "this token must pass ?project= set to one of its projects")
			return false
		}
		if !grantHas(tokenProjects(c), slug) {
			fail(c, http.StatusForbidden, "project_denied", "token cannot access project "+slug)
			return false
		}
		return true
	case "/api/v1/artifacts/:id", "/api/v1/artifacts/:id/download":
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			return true
		}
		a, err := s.Store.Artifact(c.Request.Context(), id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return true
			}
			s.internalError(c, err)
			return false
		}
		return s.enforceRunGrant(c, a.RunID)
	case "/api/v1/steps/:stepID/logs", "/api/v1/steps/:stepID/logs/stream", "/api/v1/steps/:stepID/summary":
		id, err := uuid.Parse(c.Param("stepID"))
		if err != nil {
			return true
		}
		_, runID, _, err := s.Store.StepByID(c.Request.Context(), id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return true
			}
			s.internalError(c, err)
			return false
		}
		return s.enforceRunGrant(c, runID)
	default:
		return true
	}
}

// enforceRunGrant hides runs outside the token's projects. A missing run
// falls through so the handler returns the same 404 a real miss would.
func (s *Server) enforceRunGrant(c *gin.Context, runID uuid.UUID) bool {
	run, err := s.Store.Run(c.Request.Context(), runID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return true
		}
		s.internalError(c, err)
		return false
	}
	p, err := s.Store.ProjectByID(c.Request.Context(), run.ProjectID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return true
		}
		s.internalError(c, err)
		return false
	}
	user, _ := currentUser(c)
	if p.OrgID != user.OrgID || !grantHas(tokenProjects(c), p.Slug) {
		fail(c, http.StatusNotFound, "not_found", "resource not found")
		return false
	}
	return true
}

// enforceDispatchLabels refuses a pool token whose workflow asks for a
// runner label outside the grant, or for no labels at all.
func (s *Server) enforceDispatchLabels(c *gin.Context, parsed json.RawMessage) bool {
	labels := tokenLabels(c)
	if len(labels) == 0 {
		return true
	}
	var plan workflows.Plan
	if len(parsed) == 0 || json.Unmarshal(parsed, &plan) != nil {
		fail(c, http.StatusForbidden, "label_denied", "this token is limited to a runner pool and the workflow plan could not be checked")
		return false
	}
	if msg := runsOnDenial(labels, plan.Jobs); msg != "" {
		fail(c, http.StatusForbidden, "label_denied", msg)
		return false
	}
	return true
}
