package api

import (
	"net/http"
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
	Protected         bool `json:"protected"`
	RequiredApprovals int  `json:"required_approvals"`
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
		fail(c, http.StatusBadRequest, "invalid_body", "expected {protected, required_approvals}")
		return
	}
	if req.RequiredApprovals < 0 || req.RequiredApprovals > 6 {
		fail(c, http.StatusBadRequest, "invalid_body", "required_approvals must be between 0 and 6")
		return
	}
	if req.Protected && req.RequiredApprovals < 1 {
		fail(c, http.StatusBadRequest, "invalid_body", "a protected environment needs at least 1 approval")
		return
	}
	env, err := s.Store.PutEnvironment(c.Request.Context(), p.ID, name, req.Protected, req.RequiredApprovals)
	if err != nil {
		s.internalError(c, err)
		return
	}
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "environment.write", p.Slug+"/"+name)
	c.JSON(http.StatusOK, gin.H{"environment": env})
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

// approveDeployment records the caller's approval; the job is released to the
// queue once approvals reach the environment's required count.
// ponytail: any org member counts as a reviewer; named reviewers when an environment grows an allow-list.
func (s *Server) approveDeployment(c *gin.Context) {
	user, _ := currentUser(c)
	jobID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_id", "job id must be a uuid")
		return
	}
	dep, released, err := s.Store.ApproveDeployment(c.Request.Context(), jobID, user.ID, user.Email)
	if err != nil {
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
	dep, err := s.Store.RejectDeployment(c.Request.Context(), jobID)
	if err != nil {
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
