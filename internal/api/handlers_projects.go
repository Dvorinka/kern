package api

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/dvorinka/kern/internal/github"
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
// (App installation token, else KERN_GITHUB_TOKEN).
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
	cc := s.Orch.Compiler
	cc.Resolve = s.reusableResolver(c.Request.Context(), p.LocalPath, p.RepoFullName, sha, nil)
	for _, name := range names {
		content, err := os.ReadFile(filepath.Join(pattern, name))
		if err != nil {
			continue
		}
		rel := ".github/workflows/" + name
		res, err := workflows.Compile(string(content), cc)
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
			string(content), res.Plan, res.Valid, res.Diagnostics, res.Deps); err != nil {
			s.internalError(c, err)
			return
		}
		out = append(out, synced{Path: rel, Name: res.Name, Valid: res.Valid, Diagnostics: res.Diagnostics})
	}
	s.autoDiscoverIcon(c.Request.Context(), p, sha)
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "project.sync", p.Slug)
	c.JSON(http.StatusOK, gin.H{"sha": sha, "workflows": out})
}

// syncProjectGitHub discovers .github/workflows via the GitHub contents API and
// stores each file as a compiled workflow version, mirroring the local path.
func (s *Server) syncProjectGitHub(c *gin.Context, user store.User, p store.Project) {
	ctx := c.Request.Context()
	gh, err := s.githubClient(ctx, p.RepoFullName)
	if err != nil {
		fail(c, http.StatusBadGateway, "github_fetch_failed", err.Error())
		return
	}
	ref := firstNonEmpty(p.DefaultBranch, "main")
	sha, err := gh.HeadSHA(ctx, p.RepoFullName, ref)
	if err != nil {
		fail(c, http.StatusBadGateway, "github_fetch_failed", "could not resolve "+ref+": "+err.Error())
		return
	}
	files, err := gh.ListWorkflowFiles(ctx, p.RepoFullName, ref)
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
	cc := s.Orch.Compiler
	cc.Resolve = s.reusableResolver(ctx, p.LocalPath, p.RepoFullName, sha, gh)
	for _, f := range files {
		content, err := gh.FileContent(ctx, p.RepoFullName, f.Path, sha)
		if err != nil {
			s.Log.Error("fetch workflow file", zap.String("path", f.Path), zap.Error(err))
			continue
		}
		res, err := workflows.Compile(content, cc)
		if err != nil {
			s.Log.Error("compile workflow during sync", zap.Error(err))
			continue
		}
		wf, err := s.Store.UpsertWorkflow(ctx, p.ID, f.Path, res.Name)
		if err != nil {
			s.internalError(c, err)
			return
		}
		if _, err := s.Store.PutWorkflowVersion(ctx, wf.ID, sha, content, res.Plan, res.Valid, res.Diagnostics, res.Deps); err != nil {
			s.internalError(c, err)
			return
		}
		out = append(out, synced{Path: f.Path, Name: res.Name, Valid: res.Valid, Diagnostics: res.Diagnostics})
	}
	s.autoDiscoverIcon(ctx, p, sha)
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

// prNumberFromRef extracts the pull request number from refs/pull/N/head.
func prNumberFromRef(ref string) (int, bool) {
	rest, ok := strings.CutPrefix(ref, "refs/pull/")
	if !ok {
		return 0, false
	}
	numStr, ok := strings.CutSuffix(rest, "/head")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(numStr)
	return n, err == nil && n > 0
}

type triggerRequest struct {
	Event         string         `json:"event"`
	Ref           string         `json:"ref"`
	SHA           string         `json:"sha"`
	Actor         string         `json:"actor"`
	CommitMessage string         `json:"commit_message"`
	TrustLevel    string         `json:"trust_level"`
	BundleRef     string         `json:"bundle_ref"`
	Inputs        map[string]any `json:"inputs"`
	// WorkflowPath + WorkflowYAML drive the by-path trigger route: a workflow
	// file that is not (yet) a synced workflow row — an unmerged branch file
	// or a local file shipped inline by `kern exec --workflow-file`.
	WorkflowPath string `json:"workflow_path"`
	WorkflowYAML string `json:"workflow_yaml"`
	WorkflowName string `json:"workflow_name"`
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
	// Resolve the ref to its current head and compile the workflow as it
	// exists there — the stored version is sync-time state and goes stale
	// between syncs (a manual dispatch once ran a month-old commit and the
	// month-old workflow definition because of it). GitHub-backed projects
	// get webhook parity; local-path projects keep the stored version.
	sha := s.resolveTriggerSHA(c.Request.Context(), p, req.Ref, req.SHA)
	if p.RepoFullName != "" && sha != "" {
		if v, _, verr := s.workflowAtSHA(c, p.RepoFullName, p.ID, wf, sha); verr == nil {
			version = v
		} else {
			s.Log.Warn("trigger workflow fetch failed; using stored version",
				zap.String("sha", sha), zap.Error(verr))
		}
	}
	// A manual dispatch against a PR ref must look like the webhook run it
	// replaces: synthesize the pull_request payload fields from the API or
	// payload-dependent actions (paths-filter, github-script reading
	// github.event.pull_request.number) see number=null (#176).
	if (req.Event == "pull_request" || req.Event == "pull_request_target") && p.RepoFullName != "" {
		if num, ok := prNumberFromRef(req.Ref); ok {
			if gh, gerr := s.githubClient(c.Request.Context(), p.RepoFullName); gerr == nil {
				if pr, perr := gh.PullRequest(c.Request.Context(), p.RepoFullName, num); perr == nil {
					source["pr_number"] = pr.Number
					source["head_ref"] = pr.Head.Ref
					source["head_sha"] = pr.Head.SHA
					source["head_repo"] = pr.Head.RepoFullName
					source["base_ref"] = pr.Base.Ref
					source["base_sha"] = pr.Base.SHA
				} else {
					s.Log.Warn("trigger PR metadata fetch failed; event payload will lack pull_request fields",
						zap.Int("pr", num), zap.Error(perr))
				}
			}
		}
	}
	run, err := s.Orch.StartRun(c.Request.Context(), store.CreateRunParams{
		ProjectID:         p.ID,
		WorkflowID:        wf.ID,
		WorkflowVersionID: version.ID,
		Event:             req.Event,
		Ref:               req.Ref,
		SHA:               firstNonEmpty(sha, version.CommitSHA),
		CommitMessage:     req.CommitMessage,
		Actor:             req.Actor,
		TrustLevel:        req.TrustLevel,
		Source:            source,
		Inputs:            req.Inputs,
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

// resolveTriggerSHA resolves req.Ref to its head sha for repo-backed
// projects; an explicit req.SHA always wins. Returns "" when the project is
// local or resolution fails — callers fall back to their stored version.
func (s *Server) resolveTriggerSHA(ctx context.Context, p store.Project, ref, sha string) string {
	if p.RepoFullName == "" || sha != "" {
		return sha
	}
	refName := strings.TrimPrefix(ref, "refs/heads/")
	refName = strings.TrimPrefix(refName, "refs/tags/")
	gh, err := s.githubClient(ctx, p.RepoFullName)
	if err != nil {
		return ""
	}
	resolved, err := gh.HeadSHA(ctx, p.RepoFullName, refName)
	if err != nil {
		s.Log.Warn("trigger ref resolution failed; using workflow-version sha",
			zap.String("ref", ref), zap.Error(err))
		return ""
	}
	return resolved
}

// normWorkflowPath cleans a client-supplied workflow file path into the
// repo-relative form stored on workflow rows. Empty or escaping paths are
// rejected.
func normWorkflowPath(p string) string {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	for strings.HasPrefix(p, "./") {
		p = strings.TrimPrefix(p, "./")
	}
	p = strings.TrimPrefix(p, "/")
	if p == "" || p == "." || strings.HasPrefix(p, "../") || strings.Contains(p, "/../") {
		return ""
	}
	return p
}

// triggerRunByPath starts a run for a workflow file that need not be a
// synced workflow row: an unmerged branch file (fetched at the resolved
// ref) or inline YAML shipped by `kern exec --workflow-file`. The workflow
// row is upserted so run history and versions stay first-class.
func (s *Server) triggerRunByPath(c *gin.Context) {
	user, _ := currentUser(c)
	p, err := s.Store.Project(c.Request.Context(), user.OrgID, c.Param("project"))
	if err != nil {
		s.internalError(c, err)
		return
	}
	var req triggerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_body", "invalid trigger body")
		return
	}
	path := normWorkflowPath(req.WorkflowPath)
	if path == "" {
		fail(c, http.StatusBadRequest, "invalid_path", "workflow_path is required and must be repo-relative")
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

	sha := s.resolveTriggerSHA(c.Request.Context(), p, req.Ref, req.SHA)
	content := req.WorkflowYAML
	if content == "" {
		switch {
		case p.RepoFullName != "" && sha != "":
			gh, gerr := s.githubClient(c.Request.Context(), p.RepoFullName)
			if gerr != nil {
				s.internalError(c, gerr)
				return
			}
			fc, ferr := gh.FileContent(c.Request.Context(), p.RepoFullName, path, sha)
			if ferr != nil {
				fail(c, http.StatusUnprocessableEntity, "workflow_fetch",
					"no workflow file at "+path+" on "+req.Ref+": "+ferr.Error())
				return
			}
			content = fc
		case p.LocalPath != "":
			b, rerr := os.ReadFile(filepath.Join(p.LocalPath, filepath.FromSlash(path)))
			if rerr != nil {
				fail(c, http.StatusUnprocessableEntity, "workflow_fetch",
					"no workflow file at "+path+" in the project checkout")
				return
			}
			content = string(b)
		default:
			fail(c, http.StatusUnprocessableEntity, "workflow_fetch",
				"could not resolve "+req.Ref+" to a commit; pass sha explicitly or ship workflow_yaml")
			return
		}
	}
	var gh *github.Client
	if p.RepoFullName != "" {
		if g, gerr := s.githubClient(c.Request.Context(), p.RepoFullName); gerr == nil {
			gh = g
		}
	}
	cc := s.Orch.Compiler
	cc.Resolve = s.reusableResolver(c.Request.Context(), p.LocalPath, p.RepoFullName, sha, gh)
	res, err := workflows.Compile(content, cc)
	if err != nil || !res.Valid || res.Plan == nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error":       gin.H{"code": "invalid_workflow", "message": "workflow does not compile"},
			"diagnostics": res.Diagnostics,
		})
		return
	}
	name := firstNonEmpty(req.WorkflowName, res.Name, filepath.Base(path))
	wf, err := s.Store.UpsertWorkflow(c.Request.Context(), p.ID, path, name)
	if err != nil {
		s.internalError(c, err)
		return
	}
	version, err := s.Store.PutWorkflowVersion(c.Request.Context(), wf.ID,
		firstNonEmpty(sha, "adhoc"), content, res.Plan, res.Valid, res.Diagnostics, res.Deps)
	if err != nil {
		s.internalError(c, err)
		return
	}

	source := map[string]any{
		"repo_url":       p.RepoURL,
		"repo_full_name": p.RepoFullName,
		"local_path":     p.LocalPath,
		"default_branch": p.DefaultBranch,
	}
	if req.BundleRef != "" {
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
		SHA:               firstNonEmpty(sha, version.CommitSHA),
		CommitMessage:     req.CommitMessage,
		Actor:             req.Actor,
		TrustLevel:        req.TrustLevel,
		Source:            source,
		Inputs:            req.Inputs,
	})
	if err != nil {
		s.internalError(c, err)
		return
	}
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "run.trigger", run.ID.String())
	c.JSON(http.StatusCreated, gin.H{"run": run})
}

func gitHead(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "local"
	}
	return strings.TrimSpace(string(out))
}

// setWorkflowDisabled toggles the per-workflow run gate. Disabled workflows
// keep versions and history; webhook, schedule, dispatch and rerun all refuse
// to start runs until re-enabled.
func (s *Server) setWorkflowDisabled(c *gin.Context) {
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
	var req struct {
		Disabled bool `json:"disabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_body", "invalid body")
		return
	}
	if err := s.Store.SetWorkflowDisabled(c.Request.Context(), wf.ID, req.Disabled); err != nil {
		s.internalError(c, err)
		return
	}
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "workflow.set_disabled", wf.ID.String())
	wf.Disabled = req.Disabled
	c.JSON(http.StatusOK, gin.H{"workflow": wf})
}
