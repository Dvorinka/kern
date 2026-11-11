package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/dvorinka/kern/internal/blobs"
	"github.com/dvorinka/kern/internal/config"
	"github.com/dvorinka/kern/internal/store"
)

func testGHCacheServer() *Server {
	return &Server{
		Cfg:     &config.Config{SessionSecret: "test-session-secret-0123456789abcdef"},
		ghCache: newGHCacheState(),
	}
}

func TestCacheJobTokenRoundTrip(t *testing.T) {
	s := testGHCacheServer()
	jobID := uuid.New()
	token := s.cacheJobToken(jobID)
	got, ok := s.parseCacheJobToken(token)
	if !ok || got != jobID {
		t.Fatalf("round trip failed: got %v ok=%v", got, ok)
	}
}

func TestCacheJobTokenRejects(t *testing.T) {
	s := testGHCacheServer()
	jobID := uuid.New()
	token := s.cacheJobToken(jobID)
	for name, bad := range map[string]string{
		"empty":      "",
		"garbage":    "not-a-token",
		"runner-ish": "run_abc.def",
		"wrong sig":  token[:len(token)-2] + "xx",
	} {
		if _, ok := s.parseCacheJobToken(bad); ok {
			t.Fatalf("%s: token unexpectedly accepted", name)
		}
	}
	// A token minted for another job parses fine — the route layer binds it
	// to the :jobID param, so cross-job use is rejected there.
	other := s.cacheJobToken(uuid.New())
	if got, ok := s.parseCacheJobToken(other); !ok || got == jobID {
		t.Fatalf("other-job token should parse to its own job id, got %v ok=%v", got, ok)
	}
}

func TestCacheJobTokenExpiry(t *testing.T) {
	s := testGHCacheServer()
	// Forge an expired token carrying a valid signature.
	jobID := uuid.New()
	exp := time.Now().Add(-time.Hour).Unix()
	mac := hmac.New(sha256.New, []byte(s.Cfg.SessionSecret))
	mac.Write([]byte(fmt.Sprintf("ghcache|%s|%d", jobID, exp)))
	token := fmt.Sprintf("kc1.%s.%d.%s", jobID, exp,
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))
	if _, ok := s.parseCacheJobToken(token); ok {
		t.Fatal("expired token accepted")
	}
}

// TestGHCacheReserveUploadCommitCycle drives the real reserve→chunk→commit
// path end to end. Regression: the upload temp file was opened O_WRONLY, so
// commit's Blobs.Put read failed EBADF and every save returned 500.
func TestGHCacheReserveUploadCommitCycle(t *testing.T) {
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

	user, err := st.Bootstrap(ctx, "it-ghc", "IT GHC", "ghc@kern.local", "IT", "x")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	p, err := st.CreateProject(ctx, user.OrgID, "ghc-"+uuid.NewString()[:8], "GHC", "", "mcp/repo", "", "main")
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	wf, err := st.UpsertWorkflow(ctx, p.ID, ".github/workflows/ci.yml", "CI")
	if err != nil {
		t.Fatalf("workflow: %v", err)
	}
	wfv, err := st.PutWorkflowVersion(ctx, wf.ID, "deadbeef", "on: push", nil, true, nil, nil)
	if err != nil {
		t.Fatalf("workflow version: %v", err)
	}
	run, err := st.CreateRun(ctx, store.CreateRunParams{
		ProjectID: p.ID, WorkflowID: wf.ID, WorkflowVersionID: wfv.ID,
		Event: "push", Ref: "refs/heads/main", SHA: "deadbeef",
	}, []store.JobSeed{{Key: "j1", JobKey: "j1", Name: "test", Needs: []string{}, RunsOn: []string{"linux"}}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	jobs, err := st.JobsForRun(ctx, run.ID)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs: %v len=%d", err, len(jobs))
	}
	job := jobs[0]

	bs, err := blobs.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatalf("blobs: %v", err)
	}
	s := testGHCacheServer()
	s.Store = st
	s.Blobs = bs
	s.Log = zap.NewNop()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("", s.requireCacheClient())
	g.POST("/runners/jobs/:jobID/ghcache/_apis/artifactcache/caches", s.ghCacheReserve)
	g.PATCH("/runners/jobs/:jobID/ghcache/_apis/artifactcache/caches/:cacheID", s.ghCacheUpload)
	g.POST("/runners/jobs/:jobID/ghcache/_apis/artifactcache/caches/:cacheID", s.ghCacheCommit)

	tok := s.cacheJobToken(job.ID)
	base := fmt.Sprintf("/runners/jobs/%s/ghcache/_apis/artifactcache/caches", job.ID)
	do := func(method, url, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, url, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	w := do(http.MethodPost, base, `{"key":"it-key","version":"v1","cacheSize":5}`)
	if w.Code != http.StatusOK {
		t.Fatalf("reserve: %d %s", w.Code, w.Body)
	}
	var res struct {
		CacheID int64 `json:"cacheId"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || res.CacheID == 0 {
		t.Fatalf("reserve body: %v %s", err, w.Body)
	}
	upload := base + "/" + fmt.Sprint(res.CacheID)

	req := httptest.NewRequest(http.MethodPatch, upload, strings.NewReader("hello"))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Range", "bytes 0-4/5")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}

	w = do(http.MethodPost, upload, `{"size":5}`)
	if w.Code != http.StatusOK {
		t.Fatalf("commit: %d %s", w.Code, w.Body)
	}

	entry, err := st.CacheLookup(ctx, p.ID, "it-key", nil, []string{"trusted"})
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	rc, _, err := s.Blobs.Open(entry.BlobRef)
	if err != nil {
		t.Fatalf("blob open: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != "hello" {
		t.Fatalf("blob content %q", got)
	}
}

func TestParseContentRange(t *testing.T) {
	for _, tc := range []struct {
		h          string
		start, end int64
		ok         bool
	}{
		{"bytes 0-99/*", 0, 99, true},
		{"bytes 100-199/1024", 100, 199, true},
		{"bytes 0-0/*", 0, 0, true},
		{"", 0, 0, false},
		{"bytes */*", 0, 0, false},
		{"bytes 5-3/*", 0, 0, false},
		{"items 0-9/*", 0, 0, false},
	} {
		st, en, ok := parseContentRange(tc.h)
		if ok != tc.ok || st != tc.start || en != tc.end {
			t.Fatalf("parseContentRange(%q) = %d,%d,%v; want %d,%d,%v",
				tc.h, st, en, ok, tc.start, tc.end, tc.ok)
		}
	}
}
