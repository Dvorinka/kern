package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/dvorinka/kern/internal/auth"
	"github.com/dvorinka/kern/internal/store"
)

func (s *Server) listMembers(c *gin.Context) {
	user, _ := currentUser(c)
	ctx, cancel := contextWithTimeout(c, 5*time.Second)
	defer cancel()
	members, err := s.Store.Members(ctx, user.OrgID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"members": members})
}

type createMemberRequest struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

func validMemberRole(role string) bool {
	switch role {
	case "member", "admin", "owner":
		return true
	}
	return false
}

func (s *Server) createMember(c *gin.Context) {
	user, _ := currentUser(c)
	var req createMemberRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_body", "email, name, password and role are required")
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if !strings.Contains(email, "@") || len(email) > 320 {
		fail(c, http.StatusBadRequest, "invalid_email", "a valid email address is required")
		return
	}
	if len(req.Password) < 8 {
		fail(c, http.StatusBadRequest, "weak_password", "password must be at least 8 characters")
		return
	}
	role := req.Role
	if role == "" {
		role = "member"
	}
	if !validMemberRole(role) {
		fail(c, http.StatusBadRequest, "invalid_role", "role must be member, admin or owner")
		return
	}
	// Only an owner can mint another owner.
	if role == "owner" && user.Role != "owner" {
		fail(c, http.StatusForbidden, "forbidden", "only an owner can grant the owner role")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		s.internalError(c, err)
		return
	}
	ctx, cancel := contextWithTimeout(c, 10*time.Second)
	defer cancel()
	member, err := s.Store.CreateMember(ctx, user.OrgID, email, strings.TrimSpace(req.Name), hash, role)
	if err != nil {
		if errors.Is(err, store.ErrExists) {
			fail(c, http.StatusConflict, "exists", err.Error())
			return
		}
		s.internalError(c, err)
		return
	}
	s.Store.Audit(ctx, user.OrgID, &user.ID, "member.create", member.ID.String())
	c.JSON(http.StatusCreated, gin.H{"member": member})
}

func (s *Server) patchMember(c *gin.Context) {
	user, _ := currentUser(c)
	target, err := uuid.Parse(c.Param("id"))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_id", "member id must be a UUID")
		return
	}
	var req struct {
		Role string `json:"role"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || !validMemberRole(req.Role) {
		fail(c, http.StatusBadRequest, "invalid_role", "role must be member, admin or owner")
		return
	}
	ctx, cancel := contextWithTimeout(c, 10*time.Second)
	defer cancel()
	// Only an owner may change an owner's role or grant owner to someone else.
	if req.Role == "owner" && user.Role != "owner" {
		fail(c, http.StatusForbidden, "forbidden", "only an owner can grant the owner role")
		return
	}
	if target != user.ID {
		var t store.User
		t, err = s.Store.UserByID(ctx, target)
		if err == nil && t.Role == "owner" && user.Role != "owner" {
			fail(c, http.StatusForbidden, "forbidden", "only an owner can change an owner's role")
			return
		}
	}
	err = s.Store.SetMemberRole(ctx, user.OrgID, target, req.Role)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			fail(c, http.StatusConflict, "conflict", err.Error())
			return
		}
		s.internalError(c, err)
		return
	}
	s.Store.Audit(ctx, user.OrgID, &user.ID, "member.role", target.String()+"="+req.Role)
	c.Status(http.StatusNoContent)
}

func (s *Server) deleteMember(c *gin.Context) {
	user, _ := currentUser(c)
	target, err := uuid.Parse(c.Param("id"))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_id", "member id must be a UUID")
		return
	}
	if target == user.ID {
		fail(c, http.StatusConflict, "self_remove", "cannot remove your own membership")
		return
	}
	ctx, cancel := contextWithTimeout(c, 10*time.Second)
	defer cancel()
	err = s.Store.RemoveMember(ctx, user.OrgID, target)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			fail(c, http.StatusConflict, "conflict", err.Error())
			return
		}
		s.internalError(c, err)
		return
	}
	s.Store.Audit(ctx, user.OrgID, &user.ID, "member.remove", target.String())
	c.Status(http.StatusNoContent)
}
