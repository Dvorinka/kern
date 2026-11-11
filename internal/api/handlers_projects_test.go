package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dvorinka/kern/internal/orchestrator"
	"github.com/gin-gonic/gin"
)

func TestPrNumberFromRef(t *testing.T) {
	for _, tc := range []struct {
		ref  string
		want int
		ok   bool
	}{
		{"refs/pull/422/head", 422, true},
		{"refs/pull/7/merge", 0, false},
		{"refs/heads/main", 0, false},
		{"refs/pull//head", 0, false},
	} {
		n, ok := prNumberFromRef(tc.ref)
		if n != tc.want || ok != tc.ok {
			t.Fatalf("prNumberFromRef(%q) = %d,%v want %d,%v", tc.ref, n, ok, tc.want, tc.ok)
		}
	}
}

func TestNormWorkflowPath(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"kern/ci.yml", "kern/ci.yml"},
		{".github/workflows/x.yml", ".github/workflows/x.yml"},
		{"./kern/ci.yml", "kern/ci.yml"},
		{"/kern/ci.yml", "kern/ci.yml"},
		{" kern\\ci.yml ", "kern/ci.yml"},
		{"", ""},
		{".", ""},
		{"../x.yml", ""},
		{"a/../../b.yml", ""},
		{"a/../b.yml", ""},
	} {
		if got := normWorkflowPath(tc.in); got != tc.want {
			t.Fatalf("normWorkflowPath(%q) = %q want %q", tc.in, got, tc.want)
		}
	}
}

// #289: StartRun refusing a disabled workflow must surface as a 4xx with a
// readable code, not the generic 500 that read as a seed-path crash.
func TestInternalErrorWorkflowDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	s := &Server{}
	s.internalError(c, fmt.Errorf("trigger: %w", orchestrator.ErrWorkflowDisabled))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	if !strings.Contains(w.Body.String(), "workflow_disabled") {
		t.Fatalf("body = %s, want workflow_disabled code", w.Body.String())
	}
}
