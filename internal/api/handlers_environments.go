package api

import (
	"errors"
	"net/http"
	"path"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/dvorinka/kern/internal/store"
)

// projectFromParam resolves :project (slug) inside the caller's org.
func (s *Server) projectFromParam(c *gin.Context) (store.Project, bool) {
	user, _ := currentUser(c)
	p, err := s.Store.Project(c.Request.Context(), user.OrgID, c.Param("project"))
	if err != nil {
		s.internalError(c, err)
		return store.Project{}, false
	}
	return p, true
}

func (s *Server) listEnvironments(c *gin.Context) {
	p, ok := s.projectFromParam(c)
	if !ok {
		return
	}
	envs, err := s.Store.ListEnvironments(c.Request.Context(), p.ID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"environments": envs})
}

type environmentRequest struct {
	Protected         bool     `json:"protected"`
	RequiredApprovals int      `json:"required_approvals"`
	ReviewerEmails    []string `json:"reviewer_emails"`
	WaitMinutes       int      `json:"wait_minutes"`
	BranchPolicy      string   `json:"branch_policy"`
	BranchPatterns    []string `json:"branch_patterns"`
}

func (s *Server) putEnvironment(c *gin.Context) {
	user, _ := currentUser(c)
	p, ok := s.projectFromParam(c)
	if !ok {
		return
	}
	name := strings.TrimSpace(c.Param("name"))
	if name == "" || strings.ContainsAny(name, " =") {
		fail(c, http.StatusBadRequest, "invalid_name", "environment name must not contain spaces or '='")
		return
	}
	var req environmentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_body", "expected an environment protection rule")
		return
	}
	in, emails, msg := normalizeEnvironment(req)
	if msg != "" {
		fail(c, http.StatusBadRequest, "invalid_body", msg)
		return
	}
	ids, err := s.Store.OrgUserIDsByEmail(c.Request.Context(), user.OrgID, emails)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	in.Reviewers = ids
	env, err := s.Store.PutEnvironment(c.Request.Context(), p.ID, name, in)
	if err != nil {
		s.internalError(c, err)
		return
	}
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "environment.write", p.Slug+"/"+name)
	c.JSON(http.StatusOK, gin.H{"environment": env})
}

func normalizeEnvironment(req environmentRequest) (store.EnvironmentInput, []string, string) {
	if req.RequiredApprovals < 0 || req.RequiredApprovals > 6 {
		return store.EnvironmentInput{}, nil, "required_approvals must be between 0 and 6"
	}
	if req.WaitMinutes < 0 || req.WaitMinutes > 43200 {
		return store.EnvironmentInput{}, nil, "wait_minutes must be between 0 and 43200"
	}
	policy := req.BranchPolicy
	if policy == "" {
		policy = "all"
	}
	if policy != "all" && policy != "selected" {
		return store.EnvironmentInput{}, nil, "branch_policy must be all or selected"
	}
	patterns := make([]string, 0, len(req.BranchPatterns))
	seen := map[string]bool{}
	for _, raw := range req.BranchPatterns {
		p := strings.TrimSpace(raw)
		if p == "" || seen[p] {
			continue
		}
		if strings.ContainsAny(p, " \t") || len(p) > 200 {
			return store.EnvironmentInput{}, nil, "branch pattern " + p + " is invalid"
		}
		if _, err := path.Match(p, "x"); err != nil {
			return store.EnvironmentInput{}, nil, "branch pattern " + p + " is invalid"
		}
		seen[p] = true
		patterns = append(patterns, p)
	}
	if len(patterns) > 20 {
		return store.EnvironmentInput{}, nil, "at most 20 branch patterns"
	}
	emails := make([]string, 0, len(req.ReviewerEmails))
	seenEmail := map[string]bool{}
	for _, raw := range req.ReviewerEmails {
		e := strings.ToLower(strings.TrimSpace(raw))
		if e == "" || seenEmail[e] {
			continue
		}
		seenEmail[e] = true
		emails = append(emails, e)
	}
	if len(emails) > 20 {
		return store.EnvironmentInput{}, nil, "at most 20 reviewers"
	}
	if len(emails) > 0 && req.RequiredApprovals > len(emails) {
		return store.EnvironmentInput{}, nil, "required_approvals cannot exceed the reviewer list"
	}
	if len(emails) > 0 && req.RequiredApprovals < 1 {
		return store.EnvironmentInput{}, nil, "named reviewers need at least 1 approval"
	}
	if policy == "selected" && len(patterns) == 0 {
		return store.EnvironmentInput{}, nil, "selected branches need at least one pattern"
	}
	if req.Protected && req.RequiredApprovals < 1 && req.WaitMinutes < 1 && policy != "selected" {
		return store.EnvironmentInput{}, nil, "a protected environment needs approvals, a wait, or selected branches"
	}
	return store.EnvironmentInput{
		Protected:         req.Protected,
		RequiredApprovals: req.RequiredApprovals,
		WaitMinutes:       req.WaitMinutes,
		BranchPolicy:      policy,
		BranchPatterns:    patterns,
	}, emails, ""
}

func (s *Server) deleteEnvironment(c *gin.Context) {
	user, _ := currentUser(c)
	p, ok := s.projectFromParam(c)
	if !ok {
		return
	}
	if err := s.Store.DeleteEnvironment(c.Request.Context(), p.ID, c.Param("name")); err != nil {
		s.internalError(c, err)
		return
	}
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "environment.delete", p.Slug+"/"+c.Param("name"))
	c.Status(http.StatusNoContent)
}

func (s *Server) listDeployments(c *gin.Context) {
	user, _ := currentUser(c)
	var projectID *uuid.UUID
	if scope := c.Query("project"); scope != "" {
		p, err := s.Store.Project(c.Request.Context(), user.OrgID, scope)
		if err != nil {
			s.internalError(c, err)
			return
		}
		projectID = &p.ID
	}
	limit := parseInt(c.Query("limit"), 50)
	deps, err := s.Store.ListDeployments(c.Request.Context(), user.OrgID, projectID, limit)
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"deployments": deps})
}

// approveDeployment records the caller's approval. The job is released once
// the approval count is met and the wait, if any, has elapsed. An empty
// reviewer list means any org member; owners and admins bypass a named list.
func (s *Server) approveDeployment(c *gin.Context) {
	user, _ := currentUser(c)
	jobID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_id", "job id must be a uuid")
		return
	}
	dep, released, err := s.Store.ApproveDeployment(c.Request.Context(), jobID, user.ID, user.Email, user.Role)
	if err != nil {
		if errors.Is(err, store.ErrNotReviewer) {
			fail(c, http.StatusForbidden, "not_reviewer", err.Error())
			return
		}
		if strings.Contains(err.Error(), "no pending deployment") || strings.Contains(err.Error(), "already approved") {
			fail(c, http.StatusConflict, "no_pending_approval", err.Error())
			return
		}
		s.internalError(c, err)
		return
	}
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "deployment.approve",
		dep.EnvironmentName+" job "+jobID.String())
	c.JSON(http.StatusOK, gin.H{"deployment": dep, "released": released})
}

// rejectDeployment refuses a pending deployment; the job is cancelled.
func (s *Server) rejectDeployment(c *gin.Context) {
	user, _ := currentUser(c)
	jobID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_id", "job id must be a uuid")
		return
	}
	dep, err := s.Store.RejectDeployment(c.Request.Context(), jobID, user.ID, user.Role)
	if err != nil {
		if errors.Is(err, store.ErrNotReviewer) {
			fail(c, http.StatusForbidden, "not_reviewer", err.Error())
			return
		}
		if strings.Contains(err.Error(), "no pending deployment") {
			fail(c, http.StatusConflict, "no_pending_approval", err.Error())
			return
		}
		s.internalError(c, err)
		return
	}
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "deployment.reject",
		dep.EnvironmentName+" job "+jobID.String())
	c.JSON(http.StatusOK, gin.H{"deployment": dep})
}
