package api

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/dvorinka/kern/internal/store"
)

func TestMCPArgHelpers(t *testing.T) {
	params := json.RawMessage(`{"name":"kern_trigger_run","arguments":{"project":"p1","limit":5,"inputs":{"a":1},"flag":true}}`)
	if got := mcpArgStringOr(params, "project"); got != "p1" {
		t.Fatalf("string arg = %q", got)
	}
	if got := mcpArgInt(params, "limit", 20); got != 5 {
		t.Fatalf("int arg = %d", got)
	}
	if got := mcpArgInt(params, "missing", 7); got != 7 {
		t.Fatalf("default int = %d", got)
	}
	if b, ok := mcpArgBool(params, "flag"); !ok || !b {
		t.Fatalf("bool arg = %v,%v", b, ok)
	}
	if _, ok := mcpArgBool(params, "project"); ok {
		t.Fatal("string arg must not parse as bool")
	}
	raw, ok := mcpArgRaw(params, "inputs")
	if !ok {
		t.Fatal("inputs raw missing")
	}
	var inputs map[string]any
	if err := json.Unmarshal(raw, &inputs); err != nil || inputs["a"] != float64(1) {
		t.Fatalf("inputs raw = %s (%v)", raw, err)
	}
}

// TestMCPResolveWorkflow exercises the UUID/path/name/basename resolution the
// trigger tool applies, including ambiguity and cross-project rejection.
func TestMCPResolveWorkflow(t *testing.T) {
	dsn := os.Getenv("KERN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("KERN_TEST_DATABASE_URL not set")
	}
	if !strings.Contains(dsn, "kern_test") {
		t.Skip("refusing to run against a non-kern_test database")
	}
	ctx := context.Background()
	lockTestDB(t, ctx, dsn)
	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(st.Close)

	user, err := st.Bootstrap(ctx, "it-org", "IT Org", "it@kern.local", "IT", "x")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	suffix := uuid.NewString()[:8]
	p, err := st.CreateProject(ctx, user.OrgID, "mcp-"+suffix, "MCP", "", "mcp/repo", "", "main")
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	other, err := st.CreateProject(ctx, user.OrgID, "mcp-other-"+suffix, "Other", "", "mcp/other", "", "main")
	if err != nil {
		t.Fatalf("other project: %v", err)
	}
	wfA, err := st.UpsertWorkflow(ctx, p.ID, ".github/workflows/ci.yml", "CI")
	if err != nil {
		t.Fatalf("workflow A: %v", err)
	}
	if _, err := st.UpsertWorkflow(ctx, p.ID, "nested/deep/ci.yml", "CI"); err != nil {
		t.Fatalf("workflow B: %v", err)
	}
	wfC, err := st.UpsertWorkflow(ctx, p.ID, ".github/workflows/release.yml", "Release")
	if err != nil {
		t.Fatalf("workflow C: %v", err)
	}
	foreign, err := st.UpsertWorkflow(ctx, other.ID, ".github/workflows/ci.yml", "Foreign")
	if err != nil {
		t.Fatalf("foreign workflow: %v", err)
	}

	s := &Server{Store: st}

	// UUID resolves directly.
	got, err := s.mcpResolveWorkflow(ctx, p.ID, wfA.ID.String())
	if err != nil || got.ID != wfA.ID {
		t.Fatalf("uuid resolve: %v %v", got.ID, err)
	}
	// Exact path resolves.
	got, err = s.mcpResolveWorkflow(ctx, p.ID, ".github/workflows/release.yml")
	if err != nil || got.ID != wfC.ID {
		t.Fatalf("path resolve: %v %v", got.ID, err)
	}
	// Name is ambiguous across the two "CI" workflows.
	if _, err := s.mcpResolveWorkflow(ctx, p.ID, "CI"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous name should fail, got %v", err)
	}
	// Basename ci.yml is ambiguous too.
	if _, err := s.mcpResolveWorkflow(ctx, p.ID, "ci.yml"); err == nil {
		t.Fatal("basename ci.yml should be ambiguous")
	}
	// A foreign project's workflow UUID is rejected.
	if _, err := s.mcpResolveWorkflow(ctx, p.ID, foreign.ID.String()); err == nil {
		t.Fatal("cross-project workflow must not resolve")
	}
	// Unknown names miss cleanly.
	if _, err := s.mcpResolveWorkflow(ctx, p.ID, "nope"); err == nil {
		t.Fatal("unknown workflow should fail")
	}
}
