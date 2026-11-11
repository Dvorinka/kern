package store

import (
	"testing"
	"time"
)

// RunnerEgress24h sums jobs.metrics->>egress_bytes per runner over the last
// day. Trusted jobs carry no egress key and must not count; jobs older than
// the window are out.
func TestRunnerEgress24h(t *testing.T) {
	s, ctx := testStore(t)
	org, err := s.FirstOrg(ctx)
	if err != nil {
		t.Fatalf("org: %v", err)
	}
	pID, wfID, vID := fixture(t, ctx, s)
	ra, err := s.RegisterRunner(ctx, RegisterRunnerParams{
		OrgID: org.ID, Name: "eg-a", Labels: []string{"self-hosted"}, TokenHash: "ha"})
	if err != nil {
		t.Fatal(err)
	}
	rb, err := s.RegisterRunner(ctx, RegisterRunnerParams{
		OrgID: org.ID, Name: "eg-b", Labels: []string{"self-hosted"}, TokenHash: "hb"})
	if err != nil {
		t.Fatal(err)
	}
	run, jobs := seedRun(t, ctx, s, CreateRunParams{
		ProjectID: pID, WorkflowID: wfID, WorkflowVersionID: vID,
		Event: "push", Ref: "refs/heads/main", TrustLevel: "fork",
	}, 3)
	finish := func(j Job, runnerID interface{}, metrics string, completedAt time.Time) {
		t.Helper()
		if _, err := s.pool.Exec(ctx,
			`UPDATE jobs SET runner_id = $2, metrics = $3::jsonb, status='completed', completed_at = $4 WHERE id = $1`,
			j.ID, runnerID, metrics, completedAt); err != nil {
			t.Fatalf("finish job: %v", err)
		}
	}
	now := time.Now()
	finish(jobs[0], ra.ID, `{"egress_bytes":1000}`, now)
	finish(jobs[1], ra.ID, `{"egress_bytes":2000,"cpu_pct_peak":5}`, now)
	finish(jobs[2], rb.ID, `{"cpu_pct_peak":5}`, now) // no egress key

	totals, err := s.RunnerEgress24h(ctx, org.ID)
	if err != nil {
		t.Fatal(err)
	}
	if totals[ra.ID] != 3000 {
		t.Fatalf("runner a = %d, want 3000", totals[ra.ID])
	}
	if totals[rb.ID] != 0 {
		t.Fatalf("runner b = %d, want 0 (no egress keys)", totals[rb.ID])
	}

	// Outside the window: excluded.
	finish(jobs[0], ra.ID, `{"egress_bytes":99999}`, now.Add(-48*time.Hour))
	totals, err = s.RunnerEgress24h(ctx, org.ID)
	if err != nil {
		t.Fatal(err)
	}
	if totals[ra.ID] != 2000 {
		t.Fatalf("after window shift, runner a = %d, want 2000", totals[ra.ID])
	}
	_ = run
}
