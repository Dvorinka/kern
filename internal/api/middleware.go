package api

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dvorinka/kern/internal/auth"
	"github.com/dvorinka/kern/internal/store"
)

const (
	ctxUser      = "kern.user"
	ctxAuthKind  = "kern.auth_kind"
	ctxRunner    = "kern.runner"
	ctxAPIScopes = "kern.api_scopes"
)

func contextWithTimeout(c *gin.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.Request.Context(), d)
}

// requireAuth accepts either an API token (Authorization: Bearer) or the session cookie.
// Cookie-authenticated writes must come from an allowed origin to block CSRF.
func (s *Server) requireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if token := bearer(c); token != "" {
			user, scopes, err := s.Store.UserByAPIToken(c.Request.Context(), auth.HashToken(token))
			if err != nil {
				fail(c, http.StatusUnauthorized, "invalid_token", "API token is invalid or expired")
				return
			}
			c.Set(ctxUser, user)
			c.Set(ctxAuthKind, "api_token")
			c.Set(ctxAPIScopes, scopes)
			c.Next()
			return
		}
		if cookie, err := c.Cookie(auth.SessionCookie); err == nil && cookie != "" {
			user, err := s.Store.UserBySession(c.Request.Context(), auth.HashToken(cookie))
			if err != nil {
				fail(c, http.StatusUnauthorized, "invalid_session", "session is invalid or expired")
				return
			}
			// Origin check for state-changing requests.
			if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
				if !s.originAllowed(c.GetHeader("Origin")) {
					fail(c, http.StatusForbidden, "csrf", "request origin is not allowed")
					return
				}
			}
			c.Set(ctxUser, user)
			c.Set(ctxAuthKind, "session")
			c.Next()
			return
		}
		fail(c, http.StatusUnauthorized, "unauthenticated", "authentication required")
	}
}

// roleRank orders org roles; owner outranks admin outranks member.
func roleRank(role string) int {
	switch role {
	case "owner":
		return 3
	case "admin":
		return 2
	case "member":
		return 1
	default:
		return 0
	}
}

// requireRole gates a route to users whose org role is at least min.
// It applies to sessions and API tokens alike.
func (s *Server) requireRole(min string) gin.HandlerFunc {
	need := roleRank(min)
	return func(c *gin.Context) {
		user, ok := currentUser(c)
		if !ok {
			fail(c, http.StatusUnauthorized, "unauthenticated", "authentication required")
			return
		}
		if roleRank(user.Role) < need {
			fail(c, http.StatusForbidden, "forbidden", "requires the "+min+" role")
			return
		}
		c.Next()
	}
}

// scopeRank orders API token scopes; admin outranks write outranks read.
func scopeRank(scope string) int {
	switch scope {
	case "admin":
		return 3
	case "write":
		return 2
	case "read":
		return 1
	default:
		return 0
	}
}

// scopeAllows reports whether the request carries at least the min scope.
// Session-authenticated requests carry no scopes and always pass.
func scopeAllows(c *gin.Context, min string) bool {
	if c.GetString(ctxAuthKind) != "api_token" {
		return true
	}
	have := 0
	if list, ok := c.Get(ctxAPIScopes); ok {
		if scopes, ok := list.([]string); ok {
			for _, sc := range scopes {
				if scopeRank(sc) > have {
					have = scopeRank(sc)
				}
			}
		}
	}
	return have >= scopeRank(min)
}

// requireScope constrains API tokens only — session-authenticated requests
// (the dashboard) are full-power.
func (s *Server) requireScope(min string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !scopeAllows(c, min) {
			fail(c, http.StatusForbidden, "insufficient_scope", "token requires the "+min+" scope")
			return
		}
		c.Next()
	}
}

// securityHeaders sets a strict baseline on every response. The SPA loads
// fonts from Google Fonts; everything else is same-origin.
func (s *Server) securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; "+
				"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; "+
				"font-src 'self' data: https://fonts.gstatic.com; "+
				"img-src 'self' data:; connect-src 'self'; "+
				"frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		c.Next()
	}
}

func (s *Server) originAllowed(origin string) bool {
	if origin == "" {
		// Non-browser clients do not send Origin; cookies are still HttpOnly and random.
		return s.Cfg.Env == "development"
	}
	if s.Cfg.Env == "development" {
		return true
	}
	for _, allowed := range s.Cfg.AllowedOrigins {
		if strings.EqualFold(origin, allowed) {
			return true
		}
	}
	return false
}

func bearer(c *gin.Context) string {
	h := c.GetHeader("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func currentUser(c *gin.Context) (store.User, bool) {
	v, ok := c.Get(ctxUser)
	if !ok {
		return store.User{}, false
	}
	u, ok := v.(store.User)
	return u, ok
}

// requireRunner authenticates runner agents with their per-runner token.
func (s *Server) requireRunner() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := bearer(c)
		if token == "" {
			fail(c, http.StatusUnauthorized, "unauthenticated", "runner token required")
			return
		}
		if !strings.HasPrefix(token, auth.RunnerPrefix) {
			fail(c, http.StatusUnauthorized, "invalid_token", "not a runner token")
			return
		}
		runner, err := s.Store.RunnerByToken(c.Request.Context(), auth.HashToken(token))
		if err != nil {
			fail(c, http.StatusUnauthorized, "invalid_token", "runner token is invalid or revoked")
			return
		}
		c.Set(ctxRunner, runner)
		c.Next()
	}
}

func currentRunner(c *gin.Context) (store.Runner, bool) {
	v, ok := c.Get(ctxRunner)
	if !ok {
		return store.Runner{}, false
	}
	r, ok := v.(store.Runner)
	return r, ok
}

func (s *Server) joinTokenValid(token string) bool {
	want := s.Cfg.RunnerJoinToken
	if want == "" || token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(want)) == 1
}

// loginLimiter is a small in-process brute-force guard. A distributed deployment should
// move this to the shared cache.
type loginLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
	limit    int
	window   time.Duration
}

func newLoginLimiter(limit int, window time.Duration) *loginLimiter {
	return &loginLimiter{attempts: map[string][]time.Time{}, limit: limit, window: window}
}

func (l *loginLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	kept := l.attempts[key][:0]
	for _, t := range l.attempts[key] {
		if now.Sub(t) < l.window {
			kept = append(kept, t)
		}
	}
	l.attempts[key] = kept
	return len(kept) < l.limit
}

func (l *loginLimiter) record(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.attempts[key] = append(l.attempts[key], time.Now())
}

func (l *loginLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, key)
}
