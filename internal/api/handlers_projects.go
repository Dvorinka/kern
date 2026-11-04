package api

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/dvorinka/kern/internal/store"
	"github.com/dvorinka/kern/internal/workflows"
)

func (s *Server) listProjects(c *gin.Context) {
	user, _ := currentUser(c)
	projects, err := s.Store.Projects(c.Request.Context(), user.OrgID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"projects": projects})
}

type createProjectRequest struct {
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	RepoURL       string `json:"repo_url"`
	RepoFullName  string `json:"repo_full_name"`
	LocalPath     string `json:"local_path"`
	DefaultBranch string `json:"default_branch"`
}

func (s *Server) createProject(c *gin.Context) {
	user, _ := currentUser(c)
	var req createProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_body", "invalid project body")
		return
	}
	req.Slug = strings.ToLower(strings.TrimSpace(req.Slug))
	if req.Slug == "" || strings.ContainsAny(req.Slug, " /\\") {
		fail(c, http.StatusBadRequest, "invalid_slug", "slug must be non-empty and contain no spaces or slashes")
		return
	}
	if req.RepoURL == "" && req.LocalPath == "" {
		fail(c, http.StatusBadRequest, "missing_repo", "repo_url or local_path is required")
		return
	}
	if req.Name == "" {
		req.Name = req.Slug
	}
	p, err := s.Store.CreateProject(c.Request.Context(), user.OrgID, req.Slug, req.Name,
		req.RepoURL, req.RepoFullName, req.LocalPath, req.DefaultBranch)
	if err != nil {
		s.internalError(c, err)
		return
	}
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "project.create", p.Slug)
	c.JSON(http.StatusCreated, gin.H{"project": p})
}

func (s *Server) getProject(c *gin.Context) {
	user, _ := currentUser(c)
	p, err := s.Store.Project(c.Request.Context(), user.OrgID, c.Param("project"))
	if err != nil {
		s.internalError(c, err)
		return
	}
	wfs, err := s.Store.Workflows(c.Request.Context(), p.ID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"project": p, "workflows": wfs})
}

// syncProject discovers workflow files. Local checkouts use the filesystem;
// projects bound to a GitHub repo are discovered through the contents API
// (KERN_GITHUB_TOKEN for private repos).
func (s *Server) syncProject(c *gin.Context) {
	user, _ := currentUser(c)
	p, err := s.Store.Project(c.Request.Context(), user.OrgID, c.Param("project"))
	if err != nil {
		s.internalError(c, err)
		return
	}
	if p.LocalPath == "" {
		if p.RepoFullName != "" {
			s.syncProjectGitHub(c, user, p)
			return
		}
		fail(c, http.StatusNotImplemented, "github_not_configured",
			"GitHub workflow discovery requires repo_full_name on the project (or local_path for local discovery)")
		return
	}
	info, err := os.Stat(p.LocalPath)
	if err != nil || !info.IsDir() {
		fail(c, http.StatusBadRequest, "local_path_invalid", "local_path does not point to a readable directory")
		return
	}
	sha := gitHead(p.LocalPath)

	pattern := filepath.Join(p.LocalPath, ".github", "workflows")
	entries, err := os.ReadDir(pattern)
	if err != nil {
		fail(c, http.StatusBadRequest, "no_workflows", "no .github/workflows directory in local_path")
		return
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".yml") || strings.HasSuffix(e.Name(), ".yaml") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	type synced struct {
		Path        string                 `json:"path"`
		Name        string                 `json:"name"`
		Valid       bool                   `json:"valid"`
		Diagnostics []workflows.Diagnostic `json:"diagnostics"`
	}
	out := []synced{}
	for _, name := range names {
		content, err := os.ReadFile(filepath.Join(pattern, name))
		if err != nil {
			continue
		}
		rel := ".github/workflows/" + name
		res, err := workflows.Compile(string(content), s.Orch.Compiler)
		if err != nil {
			s.Log.Error("compile workflow during sync", zap.Error(err))
			continue
		}
		wf, err := s.Store.UpsertWorkflow(c.Request.Context(), p.ID, rel, res.Name)
		if err != nil {
			s.internalError(c, err)
			return
		}
		if _, err := s.Store.PutWorkflowVersion(c.Request.Context(), wf.ID, sha,
			string(content), res.Plan, res.Valid, res.Diagnostics); err != nil {
			s.internalError(c, err)
			return
		}
		out = append(out, synced{Path: rel, Name: res.Name, Valid: res.Valid, Diagnostics: res.Diagnostics})
	}
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "project.sync", p.Slug)
	c.JSON(http.StatusOK, gin.H{"sha": sha, "workflows": out})
}

// syncProjectGitHub discovers .github/workflows via the GitHub contents API and
// stores each file as a compiled workflow version, mirroring the local path.
func (s *Server) syncProjectGitHub(c *gin.Context, user store.User, p store.Project) {
	ctx := c.Request.Context()
	ref := firstNonEmpty(p.DefaultBranch, "main")
	sha, err := s.GH.HeadSHA(ctx, p.RepoFullName, ref)
	if err != nil {
		fail(c, http.StatusBadGateway, "github_fetch_failed", "could not resolve "+ref+": "+err.Error())
		return
	}
	files, err := s.GH.ListWorkflowFiles(ctx, p.RepoFullName, ref)
	if err != nil {
		fail(c, http.StatusBadGateway, "github_fetch_failed", err.Error())
		return
	}
	if len(files) == 0 {
		fail(c, http.StatusBadRequest, "no_workflows", "no .github/workflows directory in "+p.RepoFullName)
		return
	}
	type synced struct {
		Path        string                 `json:"path"`
		Name        string                 `json:"name"`
		Valid       bool                   `json:"valid"`
		Diagnostics []workflows.Diagnostic `json:"diagnostics"`
	}
	out := []synced{}
	for _, f := range files {
		content, err := s.GH.FileContent(ctx, p.RepoFullName, f.Path, sha)
		if err != nil {
			s.Log.Error("fetch workflow file", zap.String("path", f.Path), zap.Error(err))
			continue
		}
		res, err := workflows.Compile(content, s.Orch.Compiler)
		if err != nil {
			s.Log.Error("compile workflow during sync", zap.Error(err))
			continue
		}
		wf, err := s.Store.UpsertWorkflow(ctx, p.ID, f.Path, res.Name)
		if err != nil {
			s.internalError(c, err)
			return
		}
		if _, err := s.Store.PutWorkflowVersion(ctx, wf.ID, sha, content, res.Plan, res.Valid, res.Diagnostics); err != nil {
			s.internalError(c, err)
			return
		}
		out = append(out, synced{Path: f.Path, Name: res.Name, Valid: res.Valid, Diagnostics: res.Diagnostics})
	}
	s.Store.Audit(ctx, user.OrgID, &user.ID, "project.sync", p.Slug)
	c.JSON(http.StatusOK, gin.H{"sha": sha, "workflows": out})
}

func (s *Server) listWorkflows(c *gin.Context) {
	user, _ := currentUser(c)
	p, err := s.Store.Project(c.Request.Context(), user.OrgID, c.Param("project"))
	if err != nil {
		s.internalError(c, err)
		return
	}
	wfs, err := s.Store.Workflows(c.Request.Context(), p.ID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"workflows": wfs})
}

type triggerRequest struct {
	Event         string `json:"event"`
	Ref           string `json:"ref"`
	SHA           string `json:"sha"`
	Actor         string `json:"actor"`
	CommitMessage string `json:"commit_message"`
	TrustLevel    string `json:"trust_level"`
	BundleRef     string `json:"bundle_ref"`
}

func (s *Server) triggerRun(c *gin.Context) {
	user, _ := currentUser(c)
	p, err := s.Store.Project(c.Request.Context(), user.OrgID, c.Param("project"))
	if err != nil {
		s.internalError(c, err)
		return
	}
	workflowID, err := uuid.Parse(c.Param("workflowID"))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_id", "workflow id must be a uuid")
		return
	}
	wf, err := s.Store.WorkflowByID(c.Request.Context(), workflowID)
	if err != nil || wf.ProjectID != p.ID {
		fail(c, http.StatusNotFound, "not_found", "workflow not found in this project")
		return
	}
	var req triggerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_body", "invalid trigger body")
		return
	}
	if req.Event == "" {
		req.Event = "workflow_dispatch"
	}
	if req.Ref == "" {
		req.Ref = "refs/heads/" + p.DefaultBranch
	}
	if req.Actor == "" {
		req.Actor = user.Email
	}
	if req.TrustLevel == "" {
		req.TrustLevel = "trusted"
	}
	switch req.TrustLevel {
	case "trusted", "protected", "pull_request", "fork", "deployment":
	default:
		fail(c, http.StatusBadRequest, "invalid_trust", "unknown trust level")
		return
	}

	_, version, err := s.Store.LatestWorkflowVersion(c.Request.Context(), p.ID, wf.Path)
	if err != nil {
		s.internalError(c, err)
		return
	}
	if !version.Valid {
		fail(c, http.StatusUnprocessableEntity, "invalid_workflow",
			"the stored workflow version is invalid; fix the diagnostics and sync again")
		return
	}
	source := map[string]any{
		"repo_url":       p.RepoURL,
		"repo_full_name": p.RepoFullName,
		"local_path":     p.LocalPath,
		"default_branch": p.DefaultBranch,
	}
	if req.BundleRef != "" {
		// Bundle refs are minted by PUT /projects/:project/source; only refs
		// under this project's prefix are honoured.
		prefix := "sources/" + p.Slug + "/"
		size, sizeErr := s.Blobs.Size(req.BundleRef)
		if !strings.HasPrefix(req.BundleRef, prefix) || sizeErr != nil || size == 0 {
			fail(c, http.StatusBadRequest, "invalid_bundle_ref",
				"bundle_ref does not reference an uploaded source bundle for this project")
			return
		}
		source["bundle_ref"] = req.BundleRef
	}
	run, err := s.Orch.StartRun(c.Request.Context(), store.CreateRunParams{
		ProjectID:         p.ID,
		WorkflowID:        wf.ID,
		WorkflowVersionID: version.ID,
		Event:             req.Event,
		Ref:               req.Ref,
		SHA:               firstNonEmpty(req.SHA, version.CommitSHA),
		CommitMessage:     req.CommitMessage,
		Actor:             req.Actor,
		TrustLevel:        req.TrustLevel,
		Source:            source,
	})
	if err != nil {
		s.internalError(c, err)
		return
	}
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "run.trigger", run.ID.String())
	c.JSON(http.StatusCreated, gin.H{"run": run})
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func gitHead(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "local"
	}
	return strings.TrimSpace(string(out))
}
