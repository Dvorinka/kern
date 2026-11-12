package store

import (
	"testing"
)

// #335: StepMedians prices a step from the workflow's recent history.
func TestStepMedians(t *testing.T) {
	s, ctx := testStore(t)
	pid, wid, vid := fixture(t, ctx, s)
	run, err := s.CreateRun(ctx, CreateRunParams{
		ProjectID: pid, WorkflowID: wid, WorkflowVersionID: vid,
		Event: "push", Ref: "refs/heads/main", TrustLevel: "trusted",
	}, []JobSeed{{Key: "j", JobKey: "build", Name: "build", Needs: []string{},
		RunsOn: []string{"linux"}, TimeoutSeconds: 60}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	jobs, _ := s.JobsForRun(ctx, run.ID)
	// Three historical durations for step "build": 60, 120, 240 — p50 = 120.
	for i, secs := range []int{60, 120, 240} {
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO steps (job_id, number, key, name, status, conclusion, started_at, completed_at)
			VALUES ($1, $2, 'k', 'build', 'completed', 'success',
			        now() - $3::int * interval '1 second', now())`,
			jobs[0].ID, i+1, secs); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	med, err := s.StepMedians(ctx, wid)
	if err != nil {
		t.Fatalf("medians: %v", err)
	}
	if got := med["build"]["build"]; got != 120 {
		t.Fatalf("median = %v, want 120", got)
	}
	// A different workflow reports no history for this step.
	if got := med["nope"]; len(got) != 0 {
		t.Fatalf("stray key: %v", got)
	}
}
