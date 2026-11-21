package store

// Cross-tenant isolation tests (#368): every org-scoped store path must
// treat a foreign org's ids as absent — no reads, no mutations, no
// aggregate leakage.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// secondOrg bootstraps a second tenant and returns its org id plus a
// project/workflow/version fixture inside it.
func secondOrg(t testing.TB, ctx context.Context, s *Store) (uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	u, err := s.Bootstrap(ctx, "org-b-"+uuid.NewString()[:8], "Org B", "b-"+uuid.NewString()[:8]+"@kern.local", "B", "x")
	if err != nil {
		t.Fatalf("bootstrap org B: %v", err)
	}
	orgID := u.OrgID
	p, err := s.CreateProject(ctx, orgID, "b-"+uuid.NewString()[:8], "B", "", "b/repo", "", "main")
	if err != nil {
		t.Fatalf("project B: %v", err)
	}
	wf, err := s.UpsertWorkflow(ctx, p.ID, ".github/workflows/ci.yml", "CI")
	if err != nil {
		t.Fatalf("workflow B: %v", err)
	}
	v, err := s.PutWorkflowVersion(ctx, wf.ID, "beef", "name: CI", map[string]any{}, true, nil, nil)
	if err != nil {
		t.Fatalf("version B: %v", err)
	}
	return orgID, p.ID, wf.ID, v.ID
}

func queueOne(t testing.TB, ctx context.Context, s *Store, pid, wid, vid uuid.UUID, trust string) (Run, Job) {
	t.Helper()
	run, err := s.CreateRun(ctx, CreateRunParams{
		ProjectID: pid, WorkflowID: wid, WorkflowVersionID: vid,
		Event: "push", Ref: "refs/heads/main", TrustLevel: trust,
	}, []JobSeed{{
		Key: "j1", JobKey: "build", RunsOn: []string{"ubuntu-latest"},
		Needs: []string{}, Image: "ubuntu:24.04", TimeoutSeconds: 3600,
	}})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	jobs, err := s.JobsForRun(ctx, run.ID)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs: %v %v", jobs, err)
	}
	if err := s.SetJobQueued(ctx, jobs[0].ID); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if _, err := s.PromoteQueuedRuns(ctx); err != nil {
		t.Fatalf("promote: %v", err)
	}
	return run, jobs[0]
}

func orgRunner(t testing.TB, ctx context.Context, s *Store, orgID uuid.UUID, name string) Runner {
	t.Helper()
	r, err := s.RegisterRunner(ctx, RegisterRunnerParams{
		OrgID: orgID, Name: name, Hostname: name, OS: "linux", Arch: "amd64",
		Labels: []string{"ubuntu-latest"}, TokenHash: "h-" + name + uuid.NewString()[:6],
	})
	if err != nil {
		t.Fatalf("runner %s: %v", name, err)
	}
	return r
}

// A runner must never claim another org's queued job — the claim query
// joins projects on the runner's org, and runnerJob re-checks downstream.
func TestClaimJobNeverCrossesOrgs(t *testing.T) {
	s, ctx := testStore(t)
	orgA, _ := s.FirstOrg(ctx)
	orgB, pidB, widB, vidB := secondOrg(t, ctx, s)
	pidA, widA, vidA := fixture(t, ctx, s)

	runA, _ := queueOne(t, ctx, s, pidA, widA, vidA, "trusted")
	runB, _ := queueOne(t, ctx, s, pidB, widB, vidB, "trusted")

	runnerA := orgRunner(t, ctx, s, orgA.ID, "ra")
	runnerB := orgRunner(t, ctx, s, orgB, "rb")

	claimed, err := s.ClaimJob(ctx, runnerA.ID, time.Minute)
	if err != nil || claimed == nil {
		t.Fatalf("org A claim: %v %v", claimed, err)
	}
	if claimed.Run.ID != runA.ID || claimed.Run.OrgID != orgA.ID {
		t.Fatalf("org A runner claimed foreign job: run=%s org=%s", claimed.Run.ID, claimed.Run.OrgID)
	}
	claimed, err = s.ClaimJob(ctx, runnerB.ID, time.Minute)
	if err != nil || claimed == nil {
		t.Fatalf("org B claim: %v %v", claimed, err)
	}
	if claimed.Run.ID != runB.ID || claimed.Run.OrgID != orgB {
		t.Fatalf("org B runner claimed foreign job: run=%s org=%s", claimed.Run.ID, claimed.Run.OrgID)
	}
	// Org A's queue is now empty — a second claim must not pick up org B's.
	if claimed, err = s.ClaimJob(ctx, runnerA.ID, time.Minute); err != nil || claimed != nil {
		t.Fatalf("org A second claim reached foreign job: %v %v", claimed, err)
	}
}

// Artifact reads are org-scoped: a foreign artifact id is not found, and a
// foreign run_id filter returns nothing.
func TestArtifactOrgScope(t *testing.T) {
	s, ctx := testStore(t)
	orgA, _ := s.FirstOrg(ctx)
	orgB, pidB, widB, vidB := secondOrg(t, ctx, s)

	runB, jobB := queueOne(t, ctx, s, pidB, widB, vidB, "trusted")
	art, err := s.CreateArtifactIfQuota(ctx, runB.ID, jobB.ID, "out.tgz", "artifacts/b/out.tgz",
		100, "sha", 0, 0)
	if err != nil {
		t.Fatalf("artifact: %v", err)
	}
	if _, err := s.Artifact(ctx, orgA.ID, art.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign artifact read = %v, want ErrNotFound", err)
	}
	if got, err := s.Artifact(ctx, orgB, art.ID); err != nil || got.ID != art.ID {
		t.Fatalf("own artifact read: %v %v", got.ID, err)
	}
	rows, err := s.Artifacts(ctx, orgA.ID, &runB.ID, 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("foreign run_id artifact list: %v %v", rows, err)
	}
}

// Deployment approve/reject must not mutate another org's pending
// deployment — the org join makes the row invisible and state untouched.
func TestDeploymentApprovalOrgScope(t *testing.T) {
	s, ctx := testStore(t)
	orgA, _ := s.FirstOrg(ctx)
	orgB, pidB, widB, vidB := secondOrg(t, ctx, s)
	var userBID uuid.UUID
	// Resolve org B's owner for the legitimate-path check.
	if err := s.pool.QueryRow(ctx,
		`SELECT user_id FROM org_members WHERE org_id = $1 LIMIT 1`, orgB).Scan(&userBID); err != nil {
		t.Fatalf("org B user: %v", err)
	}
	var userAID uuid.UUID
	if err := s.pool.QueryRow(ctx,
		`SELECT user_id FROM org_members WHERE org_id = $1 LIMIT 1`, orgA.ID).Scan(&userAID); err != nil {
		t.Fatalf("org A user: %v", err)
	}

	env, err := s.PutEnvironment(ctx, pidB, "prod", EnvironmentInput{Protected: true, RequiredApprovals: 1})
	if err != nil {
		t.Fatalf("env: %v", err)
	}
	// Jobs stay 'waiting' — the deployment gate parks them from there.
	runB, err := s.CreateRun(ctx, CreateRunParams{
		ProjectID: pidB, WorkflowID: widB, WorkflowVersionID: vidB,
		Event: "push", Ref: "refs/heads/main", TrustLevel: "trusted",
	}, []JobSeed{{
		Key: "j1", JobKey: "deploy", RunsOn: []string{"ubuntu-latest"},
		Needs: []string{}, Image: "ubuntu:24.04", TimeoutSeconds: 3600, Environment: "prod",
	}})
	if err != nil {
		t.Fatalf("run B: %v", err)
	}
	if _, err := s.PromoteQueuedRuns(ctx); err != nil {
		t.Fatalf("promote: %v", err)
	}
	jobs, err := s.JobsForRun(ctx, runB.ID)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs: %v %v", jobs, err)
	}
	jobB := jobs[0]
	if _, err := s.CreateDeployment(ctx, pidB, env.ID, runB.ID, jobB.ID, runB.Ref, runB.SHA, "", nil); err != nil {
		t.Fatalf("deployment: %v", err)
	}
	if err := s.SetJobWaitingApproval(ctx, jobB.ID); err != nil {
		t.Fatalf("park: %v", err)
	}

	// Foreign approve and reject must fail without touching state.
	if _, _, err := s.ApproveDeployment(ctx, orgA.ID, jobB.ID, userAID, "a@kern.local", "owner"); err == nil {
		t.Fatal("foreign approve should fail")
	}
	if _, err := s.RejectDeployment(ctx, orgA.ID, jobB.ID, userAID, "owner"); err == nil {
		t.Fatal("foreign reject should fail")
	}
	if j, _ := s.Job(ctx, jobB.ID); j.Status != "waiting_approval" {
		t.Fatalf("foreign call mutated job: %s", j.Status)
	}
	if d, _ := s.DeploymentForJob(ctx, jobB.ID); d.Status != "queued" {
		t.Fatalf("foreign call mutated deployment: %s", d.Status)
	}

	// Same-org approve still works.
	if _, released, err := s.ApproveDeployment(ctx, orgB, jobB.ID, userBID, "b@kern.local", "owner"); err != nil || !released {
		t.Fatalf("own approve: released=%v err=%v", released, err)
	}
}

// Remediation approve/dismiss on a foreign job id must look like "no
// proposal" — never reach the step UPDATEs.
func TestRemediationOrgScope(t *testing.T) {
	s, ctx := testStore(t)
	orgA, _ := s.FirstOrg(ctx)
	_, pidB, widB, vidB := secondOrg(t, ctx, s)
	_, jobB := queueOne(t, ctx, s, pidB, widB, vidB, "trusted")

	if _, err := s.ApproveRemediation(ctx, orgA.ID, jobB.ID, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign approve remediation = %v, want ErrNotFound", err)
	}
	if err := s.DismissRemediation(ctx, orgA.ID, jobB.ID, 1); err == nil {
		t.Fatal("foreign dismiss remediation should fail")
	}
}

// Webhook deliveries carry their owning org: writes stamp it, reads and
// stats filter on it, and SetWebhookDeliveryOrg is first-writer-wins so a
// redelivery cannot move a row between tenants.
func TestWebhookDeliveryOrgScope(t *testing.T) {
	s, ctx := testStore(t)
	orgA, _ := s.FirstOrg(ctx)
	orgB, _, _, _ := secondOrg(t, ctx, s)
	payload := []byte(`{"ok":true}`)
	suffix := uuid.NewString()[:8]
	delivOrgB := "d-orgb-" + suffix
	delivUnresolved := "d-unres-" + suffix

	fresh, err := s.SaveWebhookDelivery(ctx, "github", delivOrgB, "push", payload, &orgB)
	if err != nil || !fresh {
		t.Fatalf("save: %v %v", fresh, err)
	}
	rows, err := s.WebhookDeliveries(ctx, orgA.ID, "", "", 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("org A sees org B delivery: %v %v", rows, err)
	}
	rows, err = s.WebhookDeliveries(ctx, orgB, "", "", 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("org B deliveries: %v %v", rows, err)
	}
	if _, err := s.WebhookDelivery(ctx, orgA.ID, rows[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign delivery read = %v, want ErrNotFound", err)
	}
	total, _, _, err := s.WebhookDeliveryStats(ctx, orgA.ID)
	if err != nil || total != 0 {
		t.Fatalf("org A delivery stats: total=%d err=%v", total, err)
	}
	total, _, _, err = s.WebhookDeliveryStats(ctx, orgB)
	if err != nil || total != 1 {
		t.Fatalf("org B delivery stats: total=%d err=%v", total, err)
	}

	// Unresolved deliveries start org-less, then bind once — and cannot be
	// moved afterwards.
	fresh, err = s.SaveWebhookDelivery(ctx, "github", delivUnresolved, "push", payload, nil)
	if err != nil || !fresh {
		t.Fatalf("save unresolved: %v %v", fresh, err)
	}
	if err := s.SetWebhookDeliveryOrg(ctx, delivUnresolved, orgA.ID); err != nil {
		t.Fatalf("bind org: %v", err)
	}
	if err := s.SetWebhookDeliveryOrg(ctx, delivUnresolved, orgB); err != nil {
		t.Fatalf("rebind org: %v", err)
	}
	var boundOrg uuid.UUID
	if err := s.pool.QueryRow(ctx,
		`SELECT org_id FROM webhook_deliveries WHERE delivery_id = $1`, delivUnresolved).Scan(&boundOrg); err != nil {
		t.Fatalf("bound org: %v", err)
	}
	if boundOrg != orgA.ID {
		t.Fatalf("delivery moved tenants: %s want %s", boundOrg, orgA.ID)
	}
}

// Dashboard aggregates must count only the caller's org.
func TestOverviewAndRunListsOrgScope(t *testing.T) {
	s, ctx := testStore(t)
	orgA, _ := s.FirstOrg(ctx)
	orgB, pidB, widB, vidB := secondOrg(t, ctx, s)
	pidA, widA, vidA := fixture(t, ctx, s)

	runA, _ := queueOne(t, ctx, s, pidA, widA, vidA, "trusted")
	runB, _ := queueOne(t, ctx, s, pidB, widB, vidB, "trusted")
	_ = runB

	oa, err := s.Overview(ctx, orgA.ID)
	if err != nil {
		t.Fatalf("overview A: %v", err)
	}
	ob, err := s.Overview(ctx, orgB)
	if err != nil {
		t.Fatalf("overview B: %v", err)
	}
	if oa.RunsTotal != 1 || ob.RunsTotal != 1 {
		t.Fatalf("overview leaked across orgs: A=%+v B=%+v", oa, ob)
	}
	if oa.JobsQueued != 1 || ob.JobsQueued != 1 {
		t.Fatalf("job counts leaked: A=%+v B=%+v", oa, ob)
	}

	runs, total, err := s.Runs(ctx, RunFilter{OrgID: orgA.ID})
	if err != nil || total != 1 || len(runs) != 1 || runs[0].ID != runA.ID {
		t.Fatalf("runs A: n=%d total=%d err=%v", len(runs), total, err)
	}
	counts, err := s.RunCounts(ctx, orgA.ID, nil, false)
	if err != nil || counts["all"] != 1 {
		t.Fatalf("run counts A: %v %v", counts, err)
	}
	days, err := s.RunDayCounts(ctx, orgA.ID, nil, 30)
	if err != nil {
		t.Fatalf("day counts: %v", err)
	}
	var dayTotal int
	for _, d := range days {
		dayTotal += d.Total
	}
	if dayTotal != 1 {
		t.Fatalf("day counts leaked: %v", days)
	}
	queued, err := s.QueuedJobs(ctx, orgA.ID, 10)
	if err != nil || len(queued) != 1 || queued[0].RunID != runA.ID {
		t.Fatalf("queued jobs A: %v %v", queued, err)
	}

	// Prefix resolution is tenant-scoped too.
	cands, err := s.RunPrefix(ctx, orgA.ID, runB.ID.String()[:8])
	if err != nil || len(cands) != 0 {
		t.Fatalf("org A resolved org B prefix: %v %v", cands, err)
	}
	cands, err = s.RunPrefix(ctx, orgB, runB.ID.String()[:8])
	if err != nil || len(cands) != 1 || cands[0].ID != runB.ID {
		t.Fatalf("org B prefix: %v %v", cands, err)
	}

	// Advisory diagnostics stay inside the org.
	un, err := s.UnmatchedQueuedJobs(ctx, orgA.ID)
	if err != nil {
		t.Fatalf("unmatched A: %v", err)
	}
	for _, u := range un {
		if u.RunID == runB.ID {
			t.Fatalf("org A unmatched list contains org B job: %+v", u)
		}
	}
	if _, err := s.QueueSeries(ctx, orgA.ID); err != nil {
		t.Fatalf("queue series: %v", err)
	}
	if _, err := s.RecentFailures(ctx, orgA.ID, 5); err != nil {
		t.Fatalf("recent failures: %v", err)
	}
	if _, _, err := s.CacheStats(ctx, orgA.ID); err != nil {
		t.Fatalf("cache stats: %v", err)
	}
	if _, err := s.RunnerUtilization(ctx, orgA.ID); err != nil {
		t.Fatalf("runner utilization: %v", err)
	}
}
