package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func setProjectConcurrencyCap(t testing.TB, ctx context.Context, s *Store, projectID uuid.UUID, n string) {
	t.Helper()
	if err := s.PutProjectSetting(ctx, projectID, "max_concurrent_jobs", n); err != nil {
		t.Fatalf("set cap: %v", err)
	}
}

// A project holding its max_concurrent_jobs in-progress limit yields its
// queued slots: a second job from the same project stays queued while
// another project's job claims normally.
func TestClaimRespectsProjectConcurrencyCap(t *testing.T) {
	s, ctx := testStore(t)
	r := labeledRunner(t, ctx, s, "r1", []string{"linux"}, "docker")

	pid, wid, vid := fixture(t, ctx, s)
	setProjectConcurrencyCap(t, ctx, s, pid, "1")

	first := queueJob(t, ctx, s, pid, wid, vid, "trusted", false)
	c, err := s.ClaimJob(ctx, r.ID, 30*time.Second)
	if err != nil || c == nil {
		t.Fatalf("first claim: claim=%v err=%v", c, err)
	}
	if c.Job.ID != first[0].ID {
		t.Fatalf("expected first job, got %+v", c.Job)
	}

	second := queueJob(t, ctx, s, pid, wid, vid, "trusted", false)
	otherPid, otherWid, otherVid := fixture(t, ctx, s)
	other := queueJob(t, ctx, s, otherPid, otherWid, otherVid, "trusted", false)

	c, err = s.ClaimJob(ctx, r.ID, 30*time.Second)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if c == nil || c.Job.ID != other[0].ID {
		t.Fatalf("capped project's job claimed ahead of uncapped project: %+v", c)
	}
	_ = second

	// Releasing the slot unblocks the capped project's queued job.
	if _, err := s.pool.Exec(ctx,
		`UPDATE jobs SET status = 'completed', completed_at = now(), runner_id = NULL
		 WHERE id = $1`, first[0].ID); err != nil {
		t.Fatalf("complete first job: %v", err)
	}
	c, err = s.ClaimJob(ctx, r.ID, 30*time.Second)
	if err != nil {
		t.Fatalf("claim after slot freed: %v", err)
	}
	if c == nil || c.Job.ID != second[0].ID {
		t.Fatalf("capped job did not claim after slot freed: %+v", c)
	}
}

// Cap of 0 and absent both mean uncapped — unlimited concurrency as before.
func TestClaimConcurrencyCapZeroIsUncapped(t *testing.T) {
	s, ctx := testStore(t)
	r := labeledRunner(t, ctx, s, "r1", []string{"linux"}, "docker")

	pid, wid, vid := fixture(t, ctx, s)
	setProjectConcurrencyCap(t, ctx, s, pid, "0")

	for i := 0; i < 2; i++ {
		queueJob(t, ctx, s, pid, wid, vid, "trusted", false)
	}
	for i := 0; i < 2; i++ {
		c, err := s.ClaimJob(ctx, r.ID, 30*time.Second)
		if err != nil || c == nil {
			t.Fatalf("claim %d under cap=0: claim=%v err=%v", i, c, err)
		}
	}
}

// The diagnostic query must agree with the claim predicate: a queued job
// waiting on the cap is reported as capped.
func TestConcurrencyCappedJobs(t *testing.T) {
	s, ctx := testStore(t)
	r := labeledRunner(t, ctx, s, "r1", []string{"linux"}, "docker")

	pid, wid, vid := fixture(t, ctx, s)
	setProjectConcurrencyCap(t, ctx, s, pid, "1")

	queueJob(t, ctx, s, pid, wid, vid, "trusted", false)
	if _, err := s.ClaimJob(ctx, r.ID, 30*time.Second); err != nil {
		t.Fatalf("claim: %v", err)
	}
	parked := queueJob(t, ctx, s, pid, wid, vid, "trusted", false)

	capped, err := s.ConcurrencyCappedJobs(ctx)
	if err != nil {
		t.Fatalf("capped query: %v", err)
	}
	found := false
	for _, id := range capped {
		if id == parked[0].ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("queued job behind project cap not reported: %v", capped)
	}
}
