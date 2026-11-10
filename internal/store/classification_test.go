package store

import (
	"testing"
	"time"

	"encoding/json"

	"github.com/google/uuid"
)

// JobFailureKinds is ordered by the step that produced each kind, and
// FinishJob persists the classification onto the job and the run rollup.
func TestJobClassification(t *testing.T) {
	s, ctx := testStore(t)
	pid, wid, vid := fixture(t, ctx, s)
	run, jobs := seedRun(t, ctx, s, CreateRunParams{
		ProjectID: pid, WorkflowID: wid, WorkflowVersionID: vid,
		Event: "push", Ref: "refs/heads/main", TrustLevel: "trusted",
	}, 1)
	job := jobs[0]

	// Two failed steps, earlier step first: kinds must come back in
	// step order so kinds[0] is the first failure.
	for i, kind := range []string{"network", "disk_full"} {
		n := i + 1
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO steps (id, job_id, number, key, name, status)
			VALUES ($1, $2, $3, $4, $5, 'queued')`,
			uuid.New(), job.ID, n, "s", "step"); err != nil {
			t.Fatalf("seed step: %v", err)
		}
		diag, _ := json.Marshal(map[string]string{"kind": kind})
		if err := s.FinishStep(ctx, job.ID, n, "failure", nil, diag, json.RawMessage("[]")); err != nil {
			t.Fatalf("finish step: %v", err)
		}
	}
	kinds, err := s.JobFailureKinds(ctx, job.ID)
	if err != nil {
		t.Fatalf("kinds: %v", err)
	}
	if len(kinds) != 2 || kinds[0] != "network" || kinds[1] != "disk_full" {
		t.Fatalf("kinds = %v, want [network disk_full]", kinds)
	}

	r := runner(t, ctx, s, "cls")
	claimed, err := s.ClaimJob(ctx, r.ID, time.Minute)
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	ok, err := s.FinishJob(ctx, r.ID, job.ID, "failure", "boom", nil, nil, kinds[0])
	if err != nil || !ok {
		t.Fatalf("finish: ok=%v err=%v", ok, err)
	}
	j, err := s.Job(ctx, job.ID)
	if err != nil {
		t.Fatalf("job: %v", err)
	}
	if j.Classification != "network" {
		t.Fatalf("job classification = %q, want network", j.Classification)
	}
	rr, err := s.Run(ctx, run.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rr.Classification != "network" {
		t.Fatalf("run classification rollup = %q, want network", rr.Classification)
	}
}
