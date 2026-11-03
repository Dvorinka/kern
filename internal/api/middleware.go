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
