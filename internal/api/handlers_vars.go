package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/dvorinka/kern/internal/store"
)

// projectScope resolves the required ?project= query parameter to a project
// in the caller's org. Writes a 400 and returns nil when absent.
func (s *Server) projectScope(c *gin.Context, orgID uuid.UUID) *store.Project {
	scope := strings.TrimSpace(c.Query("project"))
	if scope == "" {
		fail(c, http.StatusBadRequest, "invalid_scope", "?project= is required")
		return nil
	}
	p, err := s.Store.Project(c.Request.Context(), orgID, scope)
	if err != nil {
		s.internalError(c, err)
		return nil
	}
	return &p
}

// listVars returns a project's configuration variables. Values are
// plaintext — vars are configuration, not credentials, matching GitHub.
func (s *Server) listVars(c *gin.Context) {
	user, _ := currentUser(c)
	p := s.projectScope(c, user.OrgID)
	if p == nil {
		return
	}
	rows, err := s.Store.VarsForProject(c.Request.Context(), p.ID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"vars": rows})
}

func (s *Server) putVar(c *gin.Context) {
	user, _ := currentUser(c)
	name := strings.TrimSpace(c.Param("name"))
	if name == "" || strings.ContainsAny(name, " =") {
		fail(c, http.StatusBadRequest, "invalid_name", "variable name must not contain spaces or '='")
		return
	}
	p := s.projectScope(c, user.OrgID)
	if p == nil {
		return
	}
	var req struct {
		Value string `json:"value"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_body", "variable value is required")
		return
	}
	if err := s.Store.UpsertVar(c.Request.Context(), p.ID, name, req.Value); err != nil {
		s.internalError(c, err)
		return
	}
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "var.write", p.Slug+"/"+name)
	c.Status(http.StatusNoContent)
}

func (s *Server) deleteVar(c *gin.Context) {
	user, _ := currentUser(c)
	p := s.projectScope(c, user.OrgID)
	if p == nil {
		return
	}
	name := c.Param("name")
	if err := s.Store.DeleteVar(c.Request.Context(), p.ID, name); err != nil {
		s.internalError(c, err)
		return
	}
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "var.delete", p.Slug+"/"+name)
	c.Status(http.StatusNoContent)
}
