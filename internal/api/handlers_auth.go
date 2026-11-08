package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/dvorinka/kern/internal/auth"
	"github.com/dvorinka/kern/internal/store"
)

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *Server) login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_body", "email and password are required")
		return
	}
	key := c.ClientIP() + "|" + strings.ToLower(req.Email)
	if !s.limiter.allow(key) {
		fail(c, http.StatusTooManyRequests, "rate_limited", "too many login attempts; try again later")
		return
	}
	user, err := s.Store.UserByEmail(c.Request.Context(), strings.ToLower(strings.TrimSpace(req.Email)))
	if err != nil || !auth.VerifyPassword(user.PasswordHash, req.Password) {
		s.limiter.record(key)
		fail(c, http.StatusUnauthorized, "invalid_credentials", "email or password is incorrect")
		return
	}
	s.limiter.reset(key)

	token, hash, err := auth.NewToken("")
	if err != nil {
		s.internalError(c, err)
		return
	}
	expires := time.Now().Add(s.Cfg.SessionTTL)
	if err := s.Store.CreateSession(c.Request.Context(), user.ID, hash, expires); err != nil {
		s.internalError(c, err)
		return
	}
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(auth.SessionCookie, token, int(s.Cfg.SessionTTL.Seconds()), "/", "", s.Cfg.Env == "production", true)
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "auth.login", user.Email)
	c.JSON(http.StatusOK, gin.H{"user": user, "expires_at": expires})
}

// cliToken is the kern CLI login path: password credentials in, an API token
// out. It shares the login rate limiter and never mints a session cookie, so it
// needs no CSRF origin check.
type cliTokenRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
	Days     int    `json:"days"`
}

func (s *Server) cliToken(c *gin.Context) {
	var req cliTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_body", "email and password are required")
		return
	}
	key := c.ClientIP() + "|" + strings.ToLower(req.Email)
	if !s.limiter.allow(key) {
		fail(c, http.StatusTooManyRequests, "rate_limited", "too many login attempts; try again later")
		return
	}
	user, err := s.Store.UserByEmail(c.Request.Context(), strings.ToLower(strings.TrimSpace(req.Email)))
	if err != nil || !auth.VerifyPassword(user.PasswordHash, req.Password) {
		s.limiter.record(key)
		fail(c, http.StatusUnauthorized, "invalid_credentials", "email or password is incorrect")
		return
	}
	s.limiter.reset(key)
	if req.Name == "" {
		req.Name = "kern-cli"
	}
	var expires *time.Time
	if req.Days > 0 {
		t := time.Now().Add(time.Duration(req.Days) * 24 * time.Hour)
		expires = &t
	}
	token, hash, err := auth.NewToken(auth.APIPrefix)
	if err != nil {
		s.internalError(c, err)
		return
	}
	rec, err := s.Store.CreateAPIToken(c.Request.Context(), user.ID, req.Name,
		auth.TokenPrefix(token), hash, []string{"read", "write", "admin"}, expires)
	if err != nil {
		s.internalError(c, err)
		return
	}
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "token.create", req.Name)
	c.JSON(http.StatusCreated, gin.H{"token": token, "record": rec,
		"user": gin.H{"email": user.Email, "org_id": user.OrgID}})
}

func (s *Server) logout(c *gin.Context) {
	if cookie, err := c.Cookie(auth.SessionCookie); err == nil && cookie != "" {
		_ = s.Store.DeleteSession(c.Request.Context(), auth.HashToken(cookie))
	}
	c.SetCookie(auth.SessionCookie, "", -1, "/", "", s.Cfg.Env == "production", true)
	c.Status(http.StatusNoContent)
}

func (s *Server) me(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		fail(c, http.StatusUnauthorized, "unauthenticated", "authentication required")
		return
	}
	prefs, err := s.Store.UserPrefs(c.Request.Context(), user.ID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"user": user, "prefs": prefs})
}

// putPrefs replaces the caller's preference object (theme and friends).
// The shape is client-owned; the only guard is a size cap so a runaway
// client cannot stuff megabytes into every /auth/me response.
func (s *Server) putPrefs(c *gin.Context) {
	user, _ := currentUser(c)
	var req struct {
		Prefs map[string]any `json:"prefs"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Prefs == nil {
		fail(c, http.StatusBadRequest, "invalid_body", "prefs object is required")
		return
	}
	raw, err := json.Marshal(req.Prefs)
	if err != nil || len(raw) > 8192 {
		fail(c, http.StatusBadRequest, "invalid_body", "prefs must be a JSON object under 8 KiB")
		return
	}
	if err := s.Store.PutUserPrefs(c.Request.Context(), user.ID, req.Prefs); err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"prefs": req.Prefs})
}

// currentSessionHash is the hash of the caller's session cookie, empty for
// API-token auth. It identifies "this device" in session lists without ever
// exposing the token.
func (s *Server) currentSessionHash(c *gin.Context) string {
	if cookie, err := c.Cookie(auth.SessionCookie); err == nil && cookie != "" {
		return auth.HashToken(cookie)
	}
	return ""
}

// listSessions shows the caller's live login sessions; the current one is
// flagged so the UI can label it.
func (s *Server) listSessions(c *gin.Context) {
	user, _ := currentUser(c)
	sessions, err := s.Store.ListSessions(c.Request.Context(), user.ID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	cur := s.currentSessionHash(c)
	for i := range sessions {
		sessions[i].Current = cur != "" && sessions[i].TokenHash == cur
	}
	c.JSON(http.StatusOK, gin.H{"sessions": sessions})
}

// revokeSession deletes one of the caller's own sessions by id.
func (s *Server) revokeSession(c *gin.Context) {
	user, _ := currentUser(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_id", "session id must be a uuid")
		return
	}
	if err := s.Store.DeleteSessionByID(c.Request.Context(), user.ID, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail(c, http.StatusNotFound, "not_found", "session not found")
			return
		}
		s.internalError(c, err)
		return
	}
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "session.revoke", id.String())
	c.Status(http.StatusNoContent)
}

// revokeOtherSessions signs the user out everywhere except the session making
// the call. API-token callers have no session to keep, so every web session
// is revoked — the token itself stays valid.
func (s *Server) revokeOtherSessions(c *gin.Context) {
	user, _ := currentUser(c)
	n, err := s.Store.DeleteOtherSessions(c.Request.Context(), user.ID, s.currentSessionHash(c))
	if err != nil {
		s.internalError(c, err)
		return
	}
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "session.revoke_others", fmt.Sprintf("%d revoked", n))
	c.JSON(http.StatusOK, gin.H{"revoked": n})
}

type createTokenRequest struct {
	Name   string   `json:"name"`
	Scopes []string `json:"scopes"`
	Days   int      `json:"days"`
}

func (s *Server) createToken(c *gin.Context) {
	user, _ := currentUser(c)
	var req createTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		fail(c, http.StatusBadRequest, "invalid_body", "token name is required")
		return
	}
	scopes := req.Scopes
	if len(scopes) == 0 {
		scopes = []string{"read"}
	}
	for _, sc := range scopes {
		switch sc {
		case "read", "write", "admin":
		default:
			fail(c, http.StatusBadRequest, "invalid_scope", "scopes must be read, write or admin")
			return
		}
	}
	var expires *time.Time
	if req.Days > 0 {
		t := time.Now().Add(time.Duration(req.Days) * 24 * time.Hour)
		expires = &t
	}
	token, hash, err := auth.NewToken(auth.APIPrefix)
	if err != nil {
		s.internalError(c, err)
		return
	}
	rec, err := s.Store.CreateAPIToken(c.Request.Context(), user.ID, req.Name, auth.TokenPrefix(token), hash, scopes, expires)
	if err != nil {
		s.internalError(c, err)
		return
	}
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "token.create", req.Name)
	c.JSON(http.StatusCreated, gin.H{"token": token, "record": rec})
}

func (s *Server) listTokens(c *gin.Context) {
	user, _ := currentUser(c)
	tokens, err := s.Store.ListAPITokens(c.Request.Context(), user.ID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"tokens": tokens})
}

func (s *Server) deleteToken(c *gin.Context) {
	user, _ := currentUser(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_id", "token id must be a uuid")
		return
	}
	if err := s.Store.DeleteAPIToken(c.Request.Context(), user.ID, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail(c, http.StatusNotFound, "not_found", "token not found")
			return
		}
		s.internalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
