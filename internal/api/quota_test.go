package api

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dvorinka/kern/internal/store"
)

// testDBLockKey serializes this test against internal/store's suite, which
// truncates shared tables under the same advisory lock (see
// internal/store/integration_test.go). Without it, `go test ./...` races:
// store tests DELETE rows mid-test and Bootstrap would add an org that
// FirstOrg (ORDER BY created_at LIMIT 1) could mistake for theirs.
const testDBLockKey int64 = 8424242

// lockTestDB holds the suite-wide advisory lock on a dedicated connection
// for the test's duration.
func lockTestDB(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("lock pool: %v", err)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		pool.Close()
		t.Fatalf("lock acquire: %v", err)
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, testDBLockKey); err != nil {
		conn.Release()
		pool.Close()
		t.Fatalf("lock: %v", err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, testDBLockKey)
		conn.Release()
		pool.Close()
	})
}

// TestArtifactQuotaBytes exercises the settings→bytes resolution the upload
// path feeds into CreateArtifactIfQuota: MB setting converts, absent/zero
// disables, and a cleared setting returns unlimited.
func TestArtifactQuotaBytes(t *testing.T) {
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

	suffix := uuid.NewString()[:8]
	// Reuse the store suite's org slug: FirstOrg picks the earliest org, so a
	// fresh slug here could hijack every store test that runs after us.
	user, err := st.Bootstrap(ctx, "it-org", "IT Org",
		"it@kern.local", "IT", "x")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	p, err := st.CreateProject(ctx, user.OrgID, "quota-"+suffix, "Quota", "", "quota/repo", "", "main")
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	wf, err := st.UpsertWorkflow(ctx, p.ID, ".github/workflows/quota.yml", "Quota")
	if err != nil {
		t.Fatalf("workflow: %v", err)
	}
	v, err := st.PutWorkflowVersion(ctx, wf.ID, "deadbeef", "name: Quota", map[string]any{}, true, nil, nil)
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	run, err := st.CreateRun(ctx, store.CreateRunParams{
		ProjectID: p.ID, WorkflowID: wf.ID, WorkflowVersionID: v.ID,
		Event: "push", Ref: "main", SHA: "abc123",
	}, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	s := &Server{Store: st}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/", nil)

	if got := s.artifactQuotaBytes(c, run.ID); got != 0 {
		t.Fatalf("unset quota should be unlimited, got %d", got)
	}
	if err := st.PutProjectSetting(ctx, p.ID, "artifact_quota_mb", "2"); err != nil {
		t.Fatalf("setting: %v", err)
	}
	if got := s.artifactQuotaBytes(c, run.ID); got != 2<<20 {
		t.Fatalf("2MiB setting should yield %d, got %d", int64(2<<20), got)
	}
	if err := st.PutProjectSetting(ctx, p.ID, "artifact_quota_mb", "bogus"); err != nil {
		t.Fatalf("bogus setting: %v", err)
	}
	if got := s.artifactQuotaBytes(c, run.ID); got != 0 {
		t.Fatalf("unparseable quota should be unlimited, got %d", got)
	}
}
