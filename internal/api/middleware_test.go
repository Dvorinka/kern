package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/dvorinka/kern/internal/config"
	"github.com/dvorinka/kern/internal/store"
)

func exerciseUser(kind, role string, scopes []string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(ctxUser, store.User{Role: role})
		c.Set(ctxAuthKind, kind)
		c.Set(ctxAPIScopes, scopes)
		c.Next()
	}
}

func TestRequireRole(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := &Server{Cfg: &config.Config{}}
	for _, tc := range []struct {
		role string
		want int
	}{
		{"owner", http.StatusOK},
		{"admin", http.StatusOK},
		{"member", http.StatusForbidden},
		{"", http.StatusForbidden},
	} {
		r := gin.New()
		r.GET("/x", exerciseUser("session", tc.role, nil), s.requireRole("admin"),
			func(c *gin.Context) { c.Status(http.StatusOK) })
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
		if w.Code != tc.want {
			t.Fatalf("role %q: got %d want %d", tc.role, w.Code, tc.want)
		}
	}
}

func TestRequireScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := &Server{Cfg: &config.Config{}}
	for _, tc := range []struct {
		name   string
		kind   string
		scopes []string
		min    string
		want   int
	}{
		{"session passes any level", "session", nil, "admin", http.StatusOK},
		{"read token reads", "api_token", []string{"read"}, "read", http.StatusOK},
		{"read token cannot write", "api_token", []string{"read"}, "write", http.StatusForbidden},
		{"write token cannot admin", "api_token", []string{"write"}, "admin", http.StatusForbidden},
		{"admin token does all", "api_token", []string{"admin"}, "admin", http.StatusOK},
		{"multi-scope picks highest", "api_token", []string{"read", "admin"}, "write", http.StatusOK},
		{"empty scopes fail", "api_token", nil, "read", http.StatusForbidden},
	} {
		r := gin.New()
		r.GET("/x", exerciseUser(tc.kind, "owner", tc.scopes), s.requireScope(tc.min),
			func(c *gin.Context) { c.Status(http.StatusOK) })
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
		if w.Code != tc.want {
			t.Fatalf("%s: got %d want %d", tc.name, w.Code, tc.want)
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := &Server{Cfg: &config.Config{}}
	r := gin.New()
	r.Use(s.securityHeaders())
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	for _, h := range []string{
		"X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy", "Content-Security-Policy",
	} {
		if w.Header().Get(h) == "" {
			t.Fatalf("missing header %s", h)
		}
	}
}
