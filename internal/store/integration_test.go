package store

// Integration tests against a real PostgreSQL. Set KERN_TEST_DATABASE_URL to
// run them; they skip otherwise. Each test truncates the mutable tables first,
// so the DSN must name a throwaway database — the guard below refuses the
// shared dev database, which these tests have wiped before.

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	dsn := os.Getenv("KERN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("KERN_TEST_DATABASE_URL not set")
	}
	if !strings.Contains(dsn, "kern_test") {
		t.Skip("refusing to truncate a shared database: KERN_TEST_DATABASE_URL must point at a throwaway DB whose name contains 'kern_test'")
	}
	ctx := context.Background()
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(s.Close)
	if _, err := s.Bootstrap(ctx, "it-org", "IT Org", "it@kern.local", "IT", "x"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	for _, table := range []string{
		"steps", "job_leases", "jobs", "artifacts", "cache_entries",
		"workflow_runs", "workflow_versions", "workflows", "runners",
	} {
		if _, err := s.pool.Exec(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("clean %s: %v", table, err)
		}
	}
	return s, ctx
}

// fixture creates a project + workflow + version and returns the ids.
func fixture(t *testing.T, ctx context.Context, s *Store) (uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	org, err := s.FirstOrg(ctx)
	if err != nil {
		t.Fatalf("org: %v", err)
	}
	p, err := s.CreateProject(ctx, org.ID, "it-"+uuid.NewString()[:8], "it", "", "it/repo", "", "main")
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	wf, err := s.UpsertWorkflow(ctx, p.ID, ".github/workflows/it.yml", "IT")
	if err != nil {
		t.Fatalf("workflow: %v", err)
	}
	v, err := s.PutWorkflowVersion(ctx, wf.ID, "deadbeef", "name: IT", map[string]any{}, true, nil)
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	return p.ID, wf.ID, v.ID
}

func seedRun(t *testing.T, ctx context.Context, s *Store, p CreateRunParams, nJobs int) (Run, []Job) {
	t.Helper()
	seeds := make([]JobSeed, nJobs)
	for i := range seeds {
		seeds[i] = JobSeed{
			Key: "j" + uuid.NewString()[:4], JobKey: "build", RunsOn: []string{"ubuntu-latest"},
			Needs: []string{}, Image: "ubuntu:24.04", TimeoutSeconds: 3600,
		}
	}
	run, err := s.CreateRun(ctx, p, seeds)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	jobs, err := s.JobsForRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("jobs: %v", err)
	}
	for _, j := range jobs {
		if err := s.SetJobQueued(ctx, j.ID); err != nil {
			t.Fatalf("queue job: %v", err)
		}
	}
	if _, err := s.PromoteQueuedRuns(ctx); err != nil {
		t.Fatalf("promote: %v", err)
	}
	return run, jobs
}

func runner(t *testing.T, ctx context.Context, s *Store, name string) Runner {
	t.Helper()
	org, _ := s.FirstOrg(ctx)
	r, err := s.RegisterRunner(ctx, RegisterRunnerParams{
		OrgID: org.ID, Name: name, Hostname: name, OS: "linux", Arch: "amd64",
		Labels: []string{"ubuntu-latest"}, TokenHash: "h-" + name,
	})
	if err != nil {
		t.Fatalf("runner: %v", err)
	}
	return r
}

// Two runners racing for one job must not both get it.
func TestClaimRace(t *testing.T) {
	s, ctx := testStore(t)
	pid, wid, vid := fixture(t, ctx, s)
	_, jobs := seedRun(t, ctx, s, CreateRunParams{
		ProjectID: pid, WorkflowID: wid, WorkflowVersionID: vid,
		Event: "push", Ref: "refs/heads/main", TrustLevel: "trusted",
	}, 1)
	r1 := runner(t, ctx, s, "r1")
	r2 := runner(t, ctx, s, "r2")

	var wg sync.WaitGroup
	results := make(chan *ClaimedJob, 2)
	for _, rid := range []uuid.UUID{r1.ID, r2.ID} {
		wg.Add(1)
		go func(rid uuid.UUID) {
			defer wg.Done()
			c, _ := s.ClaimJob(ctx, rid, time.Minute)
			results <- c
		}(rid)
	}
	wg.Wait()
	close(results)
	won := 0
	for c := range results {
		if c != nil {
			won++
			if c.Job.ID != jobs[0].ID {
				t.Errorf("claimed wrong job %v", c.Job.ID)
			}
		}
	}
	if won != 1 {
		t.Fatalf("claim race: %d winners, want 1", won)
	}
}

// An expired lease requeues the job; exceeding max attempts fails it.
func TestLeaseReaping(t *testing.T) {
	s, ctx := testStore(t)
	pid, wid, vid := fixture(t, ctx, s)
	_, jobs := seedRun(t, ctx, s, CreateRunParams{
		ProjectID: pid, WorkflowID: wid, WorkflowVersionID: vid,
		Event: "push", Ref: "refs/heads/main", TrustLevel: "trusted",
	}, 1)
	r := runner(t, ctx, s, "reaper")
	if _, err := s.ClaimJob(ctx, r.ID, time.Nanosecond); err != nil {
		t.Fatalf("claim: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := s.ReapExpiredLeases(ctx, 3); err != nil {
		t.Fatalf("reap: %v", err)
	}
	j, _ := s.Job(ctx, jobs[0].ID)
	if j.Status != "queued" || j.Attempt != 1 {
		t.Fatalf("after reap: status=%s attempt=%d, want queued/1", j.Status, j.Attempt)
	}

	if _, err := s.ClaimJob(ctx, r.ID, time.Nanosecond); err != nil {
		t.Fatalf("re-claim: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := s.ReapExpiredLeases(ctx, 1); err != nil {
		t.Fatalf("reap2: %v", err)
	}
	j, _ = s.Job(ctx, jobs[0].ID)
	if j.Status != "completed" || j.Conclusion != "failure" {
		t.Fatalf("after max attempts: status=%s conclusion=%s, want completed/failure", j.Status, j.Conclusion)
	}
}

// A queued run in a group with cancel-in-progress cancels the older in-group
// run; the newer one promotes.
func TestCancelInProgress(t *testing.T) {
	s, ctx := testStore(t)
	pid, wid, vid := fixture(t, ctx, s)
	params := func() CreateRunParams {
		return CreateRunParams{
			ProjectID: pid, WorkflowID: wid, WorkflowVersionID: vid,
			Event: "push", Ref: "refs/heads/main", TrustLevel: "trusted",
			ConcurrencyGroup: "g", CancelInProgress: true,
		}
	}
	older, _ := seedRun(t, ctx, s, params(), 1)
	newer, _ := seedRun(t, ctx, s, params(), 1)

	if _, err := s.PromoteQueuedRuns(ctx); err != nil {
		t.Fatalf("promote: %v", err)
	}
	o, _ := s.Run(ctx, older.ID)
	n, _ := s.Run(ctx, newer.ID)
	if o.Status != "cancelled" {
		t.Fatalf("older run = %s, want cancelled", o.Status)
	}
	if n.Status != "in_progress" {
		t.Fatalf("newer run = %s, want in_progress", n.Status)
	}
}

// Without a group, cancel-in-progress must not cancel anything (the compiler
// warns it has no effect).
func TestCancelInProgressNoGroup(t *testing.T) {
	s, ctx := testStore(t)
	pid, wid, vid := fixture(t, ctx, s)
	params := func() CreateRunParams {
		return CreateRunParams{
			ProjectID: pid, WorkflowID: wid, WorkflowVersionID: vid,
			Event: "push", Ref: "refs/heads/main", TrustLevel: "trusted",
			ConcurrencyGroup: "", CancelInProgress: true,
		}
	}
	older, _ := seedRun(t, ctx, s, params(), 1)
	newer, _ := seedRun(t, ctx, s, params(), 1)
	if _, err := s.PromoteQueuedRuns(ctx); err != nil {
		t.Fatalf("promote: %v", err)
	}
	o, _ := s.Run(ctx, older.ID)
	n, _ := s.Run(ctx, newer.ID)
	if o.Status == "cancelled" {
		t.Fatal("ungrouped run was cancelled: empty groups must never match")
	}
	if n.Status != "in_progress" {
		t.Fatalf("newer run = %s, want in_progress", n.Status)
	}
}
