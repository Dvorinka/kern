package orchestrator

// AdvanceRun tests hit PostgreSQL. They use the store suite's DSN guard:
// KERN_TEST_DATABASE_URL must name a throwaway database containing kern_test.
// CI without that DSN skips them; a local run must export it or the gate is untested.

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	"github.com/dvorinka/kern/internal/store"
	"github.com/dvorinka/kern/internal/workflows"
)

// testDBLock serializes packages that truncate kern_test. go test runs
// packages in parallel; without this, store and orchestrator wipe each other.
const testDBLock int64 = 8424242

func lockTestDB(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire test db lock: %v", err)
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, testDBLock); err != nil {
		conn.Release()
		t.Fatalf("lock test db: %v", err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, testDBLock)
		conn.Release()
	})
}

func testStore(t *testing.T) (*store.Store, context.Context) {
	t.Helper()
	dsn := os.Getenv("KERN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("KERN_TEST_DATABASE_URL not set")
	}
	if !strings.Contains(dsn, "kern_test") {
		t.Skip("refusing to truncate a shared database: KERN_TEST_DATABASE_URL must point at a throwaway DB whose name contains kern_test")
	}
	ctx := context.Background()
	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(s.Close)
	lockTestDB(t, ctx, s.Pool())
	if _, err := s.Bootstrap(ctx, "it-org", "IT Org", "it@kern.local", "IT", "x"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	for _, table := range []string{
		"steps", "job_leases", "jobs", "artifacts", "cache_entries",
		"workflow_runs", "workflow_versions", "workflows", "runners",
	} {
		if _, err := s.Pool().Exec(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("clean %s: %v", table, err)
		}
	}
	return s, ctx
}

func fixture(t *testing.T, ctx context.Context, s *store.Store) (uuid.UUID, uuid.UUID, uuid.UUID) {
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
	v, err := s.PutWorkflowVersion(ctx, wf.ID, "deadbeef", "name: IT", map[string]any{}, true, nil, nil)
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	return p.ID, wf.ID, v.ID
}

func newOrch(s *store.Store, reported *[]string) *Orchestrator {
	o := New(s, zap.NewNop(), workflows.Compiler{})
	o.PostStatus = func(_ context.Context, run store.Run, state string) {
		*reported = append(*reported, run.ID.String()+":"+state)
	}
	return o
}

func fillSeeds(seeds []store.JobSeed) []store.JobSeed {
	for i := range seeds {
		if seeds[i].Needs == nil {
			seeds[i].Needs = []string{}
		}
		if len(seeds[i].RunsOn) == 0 {
			seeds[i].RunsOn = []string{"ubuntu-latest"}
		}
		if seeds[i].Image == "" {
			seeds[i].Image = "ubuntu:24.04"
		}
	}
	return seeds
}

func startRun(t *testing.T, ctx context.Context, s *store.Store, p store.CreateRunParams, seeds []store.JobSeed) store.Run {
	t.Helper()
	run, err := s.CreateRun(ctx, p, fillSeeds(seeds))
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if _, err := s.PromoteQueuedRuns(ctx); err != nil {
		t.Fatalf("promote: %v", err)
	}
	return run
}

func jobByKey(t *testing.T, ctx context.Context, s *store.Store, runID uuid.UUID, key string) store.Job {
	t.Helper()
	jobs, err := s.JobsForRun(ctx, runID)
	if err != nil {
		t.Fatalf("jobs: %v", err)
	}
	for _, j := range jobs {
		if j.Key == key {
			return j
		}
	}
	t.Fatalf("no job %s", key)
	return store.Job{}
}

func completeJob(t *testing.T, ctx context.Context, s *store.Store, id uuid.UUID, conclusion string) {
	t.Helper()
	tag, err := s.Pool().Exec(ctx, `
		UPDATE jobs SET status = 'completed', conclusion = $2, completed_at = now()
		WHERE id = $1`, id, conclusion)
	if err != nil {
		t.Fatalf("complete job: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("complete job: %d rows", tag.RowsAffected())
	}
}

func params(pid, wid, vid uuid.UUID, ref string) store.CreateRunParams {
	return store.CreateRunParams{
		ProjectID: pid, WorkflowID: wid, WorkflowVersionID: vid,
		Event: "push", Ref: ref, SHA: "abc", TrustLevel: "trusted",
	}
}

func stepSeed() []store.StepSeed {
	return []store.StepSeed{{Key: "run", Name: "Run"}}
}

func TestAdvanceRunSkipsDependentsAfterFailure(t *testing.T) {
	s, ctx := testStore(t)
	pid, wid, vid := fixture(t, ctx, s)
	run := startRun(t, ctx, s, params(pid, wid, vid, "refs/heads/main"), []store.JobSeed{
		{Key: "build", JobKey: "build", RunsOn: []string{"ubuntu-latest"}, Image: "ubuntu:24.04", Steps: stepSeed()},
		{Key: "test", JobKey: "test", Needs: []string{"build"}, RunsOn: []string{"ubuntu-latest"}, Image: "ubuntu:24.04", Steps: stepSeed()},
	})
	var reported []string
	o := newOrch(s, &reported)

	if err := o.AdvanceRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if got := jobByKey(t, ctx, s, run.ID, "build"); got.Status != "queued" {
		t.Fatalf("build = %s, want queued", got.Status)
	}
	if got := jobByKey(t, ctx, s, run.ID, "test"); got.Status != "waiting" {
		t.Fatalf("test = %s, want waiting while build runs", got.Status)
	}

	completeJob(t, ctx, s, jobByKey(t, ctx, s, run.ID, "build").ID, "failure")
	if err := o.AdvanceRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	testJob := jobByKey(t, ctx, s, run.ID, "test")
	if testJob.Status != "skipped" || testJob.Conclusion != "skipped" {
		t.Fatalf("test = %s/%s, want skipped", testJob.Status, testJob.Conclusion)
	}
	steps, err := s.StepsForJob(ctx, testJob.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || steps[0].Conclusion != "skipped" {
		t.Fatalf("dependent steps = %+v, want skipped", steps)
	}
	finished, _ := s.Run(ctx, run.ID)
	if finished.Status != "completed" || finished.Conclusion != "failure" {
		t.Fatalf("run = %s/%s, want completed/failure", finished.Status, finished.Conclusion)
	}
	if len(reported) != 1 || reported[0] != run.ID.String()+":failure" {
		t.Fatalf("status reports = %v", reported)
	}
	if err := o.AdvanceRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if len(reported) != 1 {
		t.Fatalf("terminal run reported again: %v", reported)
	}
}

func TestAdvanceRunContinueOnErrorReleasesDependents(t *testing.T) {
	s, ctx := testStore(t)
	pid, wid, vid := fixture(t, ctx, s)
	run := startRun(t, ctx, s, params(pid, wid, vid, "refs/heads/main"), []store.JobSeed{
		{Key: "build", JobKey: "build", ContinueOnError: true, RunsOn: []string{"ubuntu-latest"}, Image: "ubuntu:24.04"},
		{Key: "test", JobKey: "test", Needs: []string{"build"}, RunsOn: []string{"ubuntu-latest"}, Image: "ubuntu:24.04"},
	})
	completeJob(t, ctx, s, jobByKey(t, ctx, s, run.ID, "build").ID, "failure")
	var reported []string
	if err := newOrch(s, &reported).AdvanceRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if got := jobByKey(t, ctx, s, run.ID, "test"); got.Status != "queued" {
		t.Fatalf("test = %s, want queued after continue-on-error", got.Status)
	}
	finished, _ := s.Run(ctx, run.ID)
	if finished.Status != "in_progress" {
		t.Fatalf("run = %s, want in_progress", finished.Status)
	}
}

func TestAdvanceRunContinueOnErrorDoesNotFailTheRun(t *testing.T) {
	s, ctx := testStore(t)
	pid, wid, vid := fixture(t, ctx, s)
	run := startRun(t, ctx, s, params(pid, wid, vid, "refs/heads/main"), []store.JobSeed{
		{Key: "build", JobKey: "build", ContinueOnError: true, RunsOn: []string{"ubuntu-latest"}, Image: "ubuntu:24.04"},
	})
	completeJob(t, ctx, s, jobByKey(t, ctx, s, run.ID, "build").ID, "failure")
	var reported []string
	if err := newOrch(s, &reported).AdvanceRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	finished, _ := s.Run(ctx, run.ID)
	if finished.Conclusion != "success" {
		t.Fatalf("run conclusion = %s, want success", finished.Conclusion)
	}
	if len(reported) != 1 || !strings.HasSuffix(reported[0], ":success") {
		t.Fatalf("status reports = %v", reported)
	}
}

func TestAdvanceRunAlwaysAndFailureConditions(t *testing.T) {
	s, ctx := testStore(t)
	pid, wid, vid := fixture(t, ctx, s)
	run := startRun(t, ctx, s, params(pid, wid, vid, "refs/heads/main"), []store.JobSeed{
		{Key: "build", JobKey: "build", RunsOn: []string{"ubuntu-latest"}, Image: "ubuntu:24.04"},
		{Key: "notify", JobKey: "notify", Needs: []string{"build"}, IfCondition: "always()", RunsOn: []string{"ubuntu-latest"}, Image: "ubuntu:24.04"},
		{Key: "onfail", JobKey: "onfail", Needs: []string{"build"}, IfCondition: "failure()", RunsOn: []string{"ubuntu-latest"}, Image: "ubuntu:24.04"},
	})
	completeJob(t, ctx, s, jobByKey(t, ctx, s, run.ID, "build").ID, "failure")
	var reported []string
	if err := newOrch(s, &reported).AdvanceRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if got := jobByKey(t, ctx, s, run.ID, "notify"); got.Status != "queued" {
		t.Fatalf("always() = %s, want queued", got.Status)
	}
	if got := jobByKey(t, ctx, s, run.ID, "onfail"); got.Status != "queued" {
		t.Fatalf("failure() = %s, want queued", got.Status)
	}
}

func TestAdvanceRunMissingNeedAndBadIfSkip(t *testing.T) {
	s, ctx := testStore(t)
	pid, wid, vid := fixture(t, ctx, s)
	run := startRun(t, ctx, s, params(pid, wid, vid, "refs/heads/main"), []store.JobSeed{
		{Key: "ghosted", JobKey: "ghosted", Needs: []string{"nope"}, RunsOn: []string{"ubuntu-latest"}, Image: "ubuntu:24.04"},
		{Key: "bad", JobKey: "bad", IfCondition: "nosuchfn(1)", RunsOn: []string{"ubuntu-latest"}, Image: "ubuntu:24.04"},
	})
	var reported []string
	if err := newOrch(s, &reported).AdvanceRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	ghosted := jobByKey(t, ctx, s, run.ID, "ghosted")
	if ghosted.Status != "skipped" || !strings.Contains(ghosted.Error, "evaluated to false") {
		t.Fatalf("missing need = %s %q", ghosted.Status, ghosted.Error)
	}
	bad := jobByKey(t, ctx, s, run.ID, "bad")
	if bad.Status != "skipped" || !strings.Contains(bad.Error, "if expression error") {
		t.Fatalf("bad if = %s %q", bad.Status, bad.Error)
	}
	finished, _ := s.Run(ctx, run.ID)
	if finished.Conclusion != "skipped" {
		t.Fatalf("run conclusion = %s, want skipped", finished.Conclusion)
	}
	if len(reported) != 1 || !strings.HasSuffix(reported[0], ":success") {
		t.Fatalf("skipped run should report a success status, got %v", reported)
	}
}

func TestAdvanceRunFailFastCancelsSiblings(t *testing.T) {
	s, ctx := testStore(t)
	pid, wid, vid := fixture(t, ctx, s)
	run := startRun(t, ctx, s, params(pid, wid, vid, "refs/heads/main"), []store.JobSeed{
		{Key: "build-1", JobKey: "build", FailFast: true, RunsOn: []string{"ubuntu-latest"}, Image: "ubuntu:24.04"},
		{Key: "build-2", JobKey: "build", FailFast: true, RunsOn: []string{"ubuntu-latest"}, Image: "ubuntu:24.04"},
	})
	completeJob(t, ctx, s, jobByKey(t, ctx, s, run.ID, "build-1").ID, "failure")
	var reported []string
	if err := newOrch(s, &reported).AdvanceRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	sib := jobByKey(t, ctx, s, run.ID, "build-2")
	if sib.Status != "cancelled" || sib.Conclusion != "cancelled" {
		t.Fatalf("sibling = %s/%s, want cancelled", sib.Status, sib.Conclusion)
	}
	finished, _ := s.Run(ctx, run.ID)
	if finished.Conclusion != "failure" {
		t.Fatalf("run conclusion = %s, want failure (failure beats cancelled)", finished.Conclusion)
	}
}

func TestAdvanceRunCancelledNeedCancelsTheRun(t *testing.T) {
	s, ctx := testStore(t)
	pid, wid, vid := fixture(t, ctx, s)
	run := startRun(t, ctx, s, params(pid, wid, vid, "refs/heads/main"), []store.JobSeed{
		{Key: "build", JobKey: "build", RunsOn: []string{"ubuntu-latest"}, Image: "ubuntu:24.04"},
		{Key: "test", JobKey: "test", Needs: []string{"build"}, RunsOn: []string{"ubuntu-latest"}, Image: "ubuntu:24.04"},
	})
	build := jobByKey(t, ctx, s, run.ID, "build")
	if _, err := s.Pool().Exec(ctx, `
		UPDATE jobs SET status = 'cancelled', conclusion = 'cancelled', completed_at = now()
		WHERE id = $1`, build.ID); err != nil {
		t.Fatal(err)
	}
	var reported []string
	if err := newOrch(s, &reported).AdvanceRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if got := jobByKey(t, ctx, s, run.ID, "test"); got.Status != "skipped" {
		t.Fatalf("dependent of cancelled = %s, want skipped", got.Status)
	}
	finished, _ := s.Run(ctx, run.ID)
	if finished.Conclusion != "cancelled" {
		t.Fatalf("run conclusion = %s, want cancelled", finished.Conclusion)
	}
	if len(reported) != 1 || !strings.HasSuffix(reported[0], ":error") {
		t.Fatalf("cancelled run should report error, got %v", reported)
	}
}

func TestAdvanceRunIgnoresQueuedRuns(t *testing.T) {
	s, ctx := testStore(t)
	pid, wid, vid := fixture(t, ctx, s)
	run, err := s.CreateRun(ctx, params(pid, wid, vid, "refs/heads/main"), fillSeeds([]store.JobSeed{
		{Key: "build", JobKey: "build"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	var reported []string
	if err := newOrch(s, &reported).AdvanceRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if got := jobByKey(t, ctx, s, run.ID, "build"); got.Status != "waiting" {
		t.Fatalf("job = %s, want waiting until the run is in progress", got.Status)
	}
	if len(reported) != 0 {
		t.Fatalf("queued run reported %v", reported)
	}
}

func TestAdvanceRunEnvironmentGates(t *testing.T) {
	s, ctx := testStore(t)
	pid, wid, vid := fixture(t, ctx, s)
	if _, err := s.PutEnvironment(ctx, pid, "prod", store.EnvironmentInput{
		Protected: true, RequiredApprovals: 1, BranchPolicy: "selected", BranchPatterns: []string{"main"},
	}); err != nil {
		t.Fatal(err)
	}
	var reported []string
	o := newOrch(s, &reported)

	openRun := startRun(t, ctx, s, params(pid, wid, vid, "refs/heads/main"), []store.JobSeed{
		{Key: "stage", JobKey: "stage", Environment: "staging", RunsOn: []string{"ubuntu-latest"}, Image: "ubuntu:24.04"},
	})
	if err := o.AdvanceRun(ctx, openRun.ID); err != nil {
		t.Fatal(err)
	}
	stage := jobByKey(t, ctx, s, openRun.ID, "stage")
	if stage.Status != "queued" {
		t.Fatalf("unprotected env job = %s, want queued", stage.Status)
	}
	dep, err := s.DeploymentForJob(ctx, stage.ID)
	if err != nil || dep.Status != "queued" || dep.EnvironmentName != "staging" {
		t.Fatalf("deployment = %+v err=%v", dep, err)
	}

	parked := startRun(t, ctx, s, params(pid, wid, vid, "refs/heads/main"), []store.JobSeed{
		{Key: "deploy", JobKey: "deploy", Environment: "prod", RunsOn: []string{"ubuntu-latest"}, Image: "ubuntu:24.04"},
	})
	if err := o.AdvanceRun(ctx, parked.ID); err != nil {
		t.Fatal(err)
	}
	if err := o.AdvanceRun(ctx, parked.ID); err != nil {
		t.Fatal(err)
	}
	deploy := jobByKey(t, ctx, s, parked.ID, "deploy")
	if deploy.Status != "waiting_approval" {
		t.Fatalf("protected job = %s, want waiting_approval", deploy.Status)
	}
	var n int
	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM deployments WHERE job_id = $1`, deploy.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("deployments = %d, want 1 after a second tick", n)
	}

	denied := startRun(t, ctx, s, params(pid, wid, vid, "refs/heads/feature"), []store.JobSeed{
		{Key: "ship", JobKey: "ship", Environment: "prod", RunsOn: []string{"ubuntu-latest"}, Image: "ubuntu:24.04", Steps: stepSeed()},
	})
	if err := o.AdvanceRun(ctx, denied.ID); err != nil {
		t.Fatal(err)
	}
	ship := jobByKey(t, ctx, s, denied.ID, "ship")
	if ship.Status != "completed" || ship.Conclusion != "failure" || !strings.Contains(ship.Error, "not allowed") {
		t.Fatalf("denied job = %s/%s %q", ship.Status, ship.Conclusion, ship.Error)
	}
	deniedDep, err := s.DeploymentForJob(ctx, ship.ID)
	if err != nil || deniedDep.Status != "failure" {
		t.Fatalf("denied deployment = %+v err=%v", deniedDep, err)
	}
	steps, _ := s.StepsForJob(ctx, ship.ID)
	if len(steps) != 1 || steps[0].Conclusion != "cancelled" {
		t.Fatalf("denied steps = %+v", steps)
	}
	finished, _ := s.Run(ctx, denied.ID)
	if finished.Conclusion != "failure" {
		t.Fatalf("denied run = %s", finished.Conclusion)
	}
}
