package store

// Integration tests against a real PostgreSQL. Set KERN_TEST_DATABASE_URL to
// run them; they skip otherwise. Each test truncates the mutable tables first,
// so the DSN must name a throwaway database — the guard below refuses the
// shared dev database, which these tests have wiped before.

import (
	"context"
	"errors"
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

// Protected environments park their job until enough approvals land; approval
// releases it to the queue, rejection cancels it.
func TestEnvironmentApprovalGate(t *testing.T) {
	s, ctx := testStore(t)
	pid, wid, vid := fixture(t, ctx, s)
	env, err := s.PutEnvironment(ctx, pid, "prod", EnvironmentInput{Protected: true, RequiredApprovals: 1})
	if err != nil {
		t.Fatalf("put environment: %v", err)
	}
	run, err := s.CreateRun(ctx, CreateRunParams{
		ProjectID: pid, WorkflowID: wid, WorkflowVersionID: vid,
		Event: "push", Ref: "refs/heads/main", SHA: "abc", TrustLevel: "trusted",
	}, []JobSeed{{
		Key: "deploy", JobKey: "deploy", RunsOn: []string{"ubuntu-latest"},
		Needs: []string{}, Image: "ubuntu:24.04", TimeoutSeconds: 3600,
		Environment: "prod", EnvironmentURL: "https://prod.example",
	}})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	jobs, err := s.JobsForRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("jobs: %v", err)
	}
	job := jobs[0]
	if job.Environment != "prod" || job.EnvironmentURL != "https://prod.example" {
		t.Fatalf("job env = %q/%q", job.Environment, job.EnvironmentURL)
	}
	if _, err := s.PromoteQueuedRuns(ctx); err != nil {
		t.Fatalf("promote: %v", err)
	}
	dep, err := s.CreateDeployment(ctx, pid, env.ID, run.ID, job.ID, run.Ref, run.SHA, job.EnvironmentURL, nil)
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	if dep.Status != "queued" {
		t.Fatalf("deployment status = %s", dep.Status)
	}
	if err := s.SetJobWaitingApproval(ctx, job.ID); err != nil {
		t.Fatalf("park job: %v", err)
	}
	r := runner(t, ctx, s, "deployer")
	if claimed, err := s.ClaimJob(ctx, r.ID, time.Minute); err != nil || claimed != nil {
		t.Fatal("parked job must not be claimable")
	}

	var userID uuid.UUID
	if err := s.pool.QueryRow(ctx, `SELECT id FROM users LIMIT 1`).Scan(&userID); err != nil {
		t.Fatalf("user: %v", err)
	}
	dep, released, err := s.ApproveDeployment(ctx, job.ID, userID, "it@kern.local", "member")
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if !released {
		t.Fatal("single required approval should release the job")
	}
	if len(dep.Approvals) == 0 || string(dep.Approvals) == "[]" {
		t.Fatal("approval was not recorded")
	}
	j, _ := s.Job(ctx, job.ID)
	if j.Status != "queued" {
		t.Fatalf("job = %s, want queued", j.Status)
	}
	if _, _, err := s.ApproveDeployment(ctx, job.ID, userID, "it@kern.local", "member"); err == nil {
		t.Fatal("double approval should fail")
	}
	claimed, err := s.ClaimJob(ctx, r.ID, time.Minute)
	if err != nil || claimed == nil {
		t.Fatalf("approved job should be claimable: %v", err)
	}
	if got, _ := s.DeploymentForJob(ctx, job.ID); got.Status != "in_progress" {
		t.Fatalf("deployment = %s, want in_progress", got.Status)
	}
}

func TestDeploymentReject(t *testing.T) {
	s, ctx := testStore(t)
	pid, wid, vid := fixture(t, ctx, s)
	env, err := s.PutEnvironment(ctx, pid, "prod", EnvironmentInput{Protected: true, RequiredApprovals: 1})
	if err != nil {
		t.Fatalf("put environment: %v", err)
	}
	run, err := s.CreateRun(ctx, CreateRunParams{
		ProjectID: pid, WorkflowID: wid, WorkflowVersionID: vid,
		Event: "push", Ref: "refs/heads/main", TrustLevel: "trusted",
	}, []JobSeed{{
		Key: "deploy", JobKey: "deploy", RunsOn: []string{"ubuntu-latest"},
		Needs: []string{}, Image: "ubuntu:24.04", TimeoutSeconds: 3600, Environment: "prod",
		Steps: []StepSeed{{Key: "1", Name: "ship"}},
	}})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	jobs, _ := s.JobsForRun(ctx, run.ID)
	job := jobs[0]
	if _, err := s.CreateDeployment(ctx, pid, env.ID, run.ID, job.ID, run.Ref, run.SHA, "", nil); err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	if err := s.SetJobWaitingApproval(ctx, job.ID); err != nil {
		t.Fatalf("park: %v", err)
	}
	var userID uuid.UUID
	if err := s.pool.QueryRow(ctx, `SELECT id FROM users LIMIT 1`).Scan(&userID); err != nil {
		t.Fatalf("user: %v", err)
	}
	dep, err := s.RejectDeployment(ctx, job.ID, userID, "member")
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if dep.Status != "failure" {
		t.Fatalf("deployment = %s, want failure", dep.Status)
	}
	j, _ := s.Job(ctx, job.ID)
	if j.Status != "cancelled" || j.Conclusion != "cancelled" {
		t.Fatalf("job = %s/%s, want cancelled", j.Status, j.Conclusion)
	}
}

func TestEnvironmentRules(t *testing.T) {
	s, ctx := testStore(t)
	pid, wid, vid := fixture(t, ctx, s)
	var userID uuid.UUID
	if err := s.pool.QueryRow(ctx, `SELECT id FROM users WHERE email = 'it@kern.local'`).Scan(&userID); err != nil {
		t.Fatalf("user: %v", err)
	}
	other := uuid.New()
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO users (id, email, name, password_hash) VALUES ($1, $2, 'other', 'x')`,
		other, "other-"+other.String()[:8]+"@kern.local"); err != nil {
		t.Fatalf("other user: %v", err)
	}

	env, err := s.PutEnvironment(ctx, pid, "prod", EnvironmentInput{
		Protected: true, RequiredApprovals: 1, Reviewers: []string{userID.String()},
		WaitMinutes: 30, BranchPolicy: "selected", BranchPatterns: []string{"main", "release/*"},
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if len(env.ReviewerEmails) != 1 || env.ReviewerEmails[0] != "it@kern.local" {
		t.Fatalf("reviewer emails = %v", env.ReviewerEmails)
	}
	if !BranchAllowed(env.BranchPolicy, env.BranchPatterns, "refs/heads/main") {
		t.Fatal("main should be allowed")
	}
	if BranchAllowed(env.BranchPolicy, env.BranchPatterns, "refs/heads/feature") {
		t.Fatal("feature should be refused")
	}
	if !BranchAllowed(env.BranchPolicy, env.BranchPatterns, "refs/heads/release/1") {
		t.Fatal("release/1 should be allowed")
	}

	run, err := s.CreateRun(ctx, CreateRunParams{
		ProjectID: pid, WorkflowID: wid, WorkflowVersionID: vid,
		Event: "push", Ref: "refs/heads/feature", SHA: "abc", TrustLevel: "trusted",
	}, []JobSeed{{
		Key: "deploy", JobKey: "deploy", RunsOn: []string{"ubuntu-latest"},
		Needs: []string{}, Image: "ubuntu:24.04", TimeoutSeconds: 3600, Environment: "prod",
	}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	jobs, _ := s.JobsForRun(ctx, run.ID)
	job := jobs[0]
	if _, err := s.CreateDeployment(ctx, pid, env.ID, run.ID, job.ID, run.Ref, run.SHA, "", nil); err != nil {
		t.Fatalf("deployment: %v", err)
	}
	if err := s.FailDeployment(ctx, job.ID, "branch feature is not allowed to deploy to prod"); err != nil {
		t.Fatalf("fail: %v", err)
	}
	j, _ := s.Job(ctx, job.ID)
	if j.Status != "completed" || j.Conclusion != "failure" {
		t.Fatalf("denied job = %s/%s", j.Status, j.Conclusion)
	}

	future := time.Now().Add(time.Hour)
	run2, err := s.CreateRun(ctx, CreateRunParams{
		ProjectID: pid, WorkflowID: wid, WorkflowVersionID: vid,
		Event: "push", Ref: "refs/heads/main", SHA: "def", TrustLevel: "trusted",
	}, []JobSeed{{
		Key: "ship", JobKey: "ship", RunsOn: []string{"ubuntu-latest"},
		Needs: []string{}, Image: "ubuntu:24.04", TimeoutSeconds: 3600, Environment: "prod",
	}})
	if err != nil {
		t.Fatalf("run2: %v", err)
	}
	jobs, _ = s.JobsForRun(ctx, run2.ID)
	job = jobs[0]
	if _, err := s.CreateDeployment(ctx, pid, env.ID, run2.ID, job.ID, run2.Ref, run2.SHA, "", &future); err != nil {
		t.Fatalf("deployment2: %v", err)
	}
	if err := s.SetJobWaitingApproval(ctx, job.ID); err != nil {
		t.Fatalf("park: %v", err)
	}
	if _, _, err := s.ApproveDeployment(ctx, job.ID, other, "other@kern.local", "member"); !errors.Is(err, ErrNotReviewer) {
		t.Fatalf("outsider approve = %v, want ErrNotReviewer", err)
	}
	if _, released, err := s.ApproveDeployment(ctx, job.ID, userID, "it@kern.local", "member"); err != nil || released {
		t.Fatalf("approve during wait: released=%v err=%v", released, err)
	}
	if n, err := s.ReleaseDueDeployments(ctx); err != nil || n != 0 {
		t.Fatalf("release during wait: n=%d err=%v", n, err)
	}
	j, _ = s.Job(ctx, job.ID)
	if j.Status != "waiting_approval" {
		t.Fatalf("job during wait = %s", j.Status)
	}

	past := time.Now().Add(-time.Minute)
	if _, err := s.pool.Exec(ctx, `UPDATE deployments SET wait_until = $2 WHERE job_id = $1`, job.ID, past); err != nil {
		t.Fatalf("rewind wait: %v", err)
	}
	n, err := s.ReleaseDueDeployments(ctx)
	if err != nil || n != 1 {
		t.Fatalf("release after wait: n=%d err=%v", n, err)
	}
	j, _ = s.Job(ctx, job.ID)
	if j.Status != "queued" {
		t.Fatalf("job after wait = %s, want queued", j.Status)
	}
}

func TestFleetSlots(t *testing.T) {
	s, ctx := testStore(t)
	org, err := s.FirstOrg(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reg := func(name string, cpus int, labels []string) {
		t.Helper()
		if _, err := s.RegisterRunner(ctx, RegisterRunnerParams{
			OrgID: org.ID, Name: name, Hostname: name, OS: "linux", Arch: "amd64",
			CPUs: cpus, Labels: labels, TokenHash: "h-" + name,
		}); err != nil {
			t.Fatal(err)
		}
	}
	reg("a", 4, []string{"ubuntu-latest"})
	reg("b", 2, []string{"ubuntu-latest", "linux"})
	reg("c", 8, []string{"linux"})
	reg("zero", 0, []string{"ubuntu-latest"})
	reg("drain", 16, []string{"ubuntu-latest"})
	reg("off", 32, []string{"ubuntu-latest"})
	listed, err := s.Runners(ctx, org.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range listed {
		if r.Name == "drain" {
			if err := s.SetRunnerDraining(ctx, org.ID, r.ID, true); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := s.pool.Exec(ctx, `UPDATE runners SET status = 'offline' WHERE name = 'off'`); err != nil {
		t.Fatal(err)
	}

	got := func(labels []string) int {
		t.Helper()
		n, err := s.FleetSlots(ctx, org.ID, labels)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := got([]string{"ubuntu-latest"}); n != 7 { // 4 + 2 + zero-as-1
		t.Fatalf("ubuntu-latest slots = %d, want 7", n)
	}
	if n := got([]string{"linux"}); n != 10 {
		t.Fatalf("linux slots = %d, want 10", n)
	}
	if n := got([]string{"ubuntu-latest", "linux"}); n != 2 {
		t.Fatalf("both labels slots = %d, want 2", n)
	}
	if n := got(nil); n != 15 { // empty runs-on matches every online runner
		t.Fatalf("empty runs-on slots = %d, want 15", n)
	}
	if n := got([]string{"gpu"}); n != 0 {
		t.Fatalf("gpu slots = %d, want 0", n)
	}
}
