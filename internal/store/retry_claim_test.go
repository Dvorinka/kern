package store

import (
	"testing"
	"time"
)

// #332: a job whose last attempt ran on this runner prefers a fresh host —
// the failed runner only retakes it when no eligible peer is online.
func TestClaimRetryAntiAffinity(t *testing.T) {
	s, ctx := testStore(t)
	fat := labeledRunner(t, ctx, s, "fat", []string{"linux"}, "docker")
	lean := labeledRunner(t, ctx, s, "lean", []string{"linux"}, "docker")

	pid, wid, vid := fixture(t, ctx, s)
	jobs := queueJob(t, ctx, s, pid, wid, vid, "trusted", false)

	c, err := s.ClaimJob(ctx, fat.ID, 30*time.Second)
	if err != nil || c == nil {
		t.Fatalf("first claim: claim=%v err=%v", c, err)
	}
	ok, err := s.RequeueJob(ctx, fat.ID, jobs[0].ID, "transient failure: auto-requeued")
	if err != nil || !ok {
		t.Fatalf("requeue: ok=%v err=%v", ok, err)
	}

	// The runner it failed on must not reclaim while a peer is eligible.
	if c, err := s.ClaimJob(ctx, fat.ID, 30*time.Second); err != nil || c != nil {
		t.Fatalf("failed runner reclaimed over eligible peer: claim=%v err=%v", c, err)
	}
	c, err = s.ClaimJob(ctx, lean.ID, 30*time.Second)
	if err != nil {
		t.Fatalf("peer claim: %v", err)
	}
	if c == nil || c.Job.ID != jobs[0].ID {
		t.Fatalf("peer did not get requeued job: %+v", c)
	}
}

// With no eligible peer online the failed runner retakes its own requeue —
// the preference is soft, never a hard exclusion.
func TestClaimRetryAntiAffinitySolo(t *testing.T) {
	s, ctx := testStore(t)
	only := labeledRunner(t, ctx, s, "only", []string{"linux"}, "docker")
	dead := labeledRunner(t, ctx, s, "dead", []string{"linux"}, "docker")
	if _, err := s.pool.Exec(ctx,
		`UPDATE runners SET status = 'offline' WHERE id = $1`, dead.ID); err != nil {
		t.Fatalf("offline peer: %v", err)
	}

	pid, wid, vid := fixture(t, ctx, s)
	jobs := queueJob(t, ctx, s, pid, wid, vid, "trusted", false)

	c, err := s.ClaimJob(ctx, only.ID, 30*time.Second)
	if err != nil || c == nil {
		t.Fatalf("first claim: claim=%v err=%v", c, err)
	}
	ok, err := s.RequeueJob(ctx, only.ID, jobs[0].ID, "transient failure: auto-requeued")
	if err != nil || !ok {
		t.Fatalf("requeue: ok=%v err=%v", ok, err)
	}
	c, err = s.ClaimJob(ctx, only.ID, 30*time.Second)
	if err != nil {
		t.Fatalf("retake claim: %v", err)
	}
	if c == nil || c.Job.ID != jobs[0].ID {
		t.Fatalf("solo runner could not retake its own requeue: %+v", c)
	}
}

// Ping-pong guard: after attempts on both runners of a two-runner fleet,
// the anti-affinity must still let the other runner take the job — only
// the LAST attempt's runner is penalized.
func TestClaimRetryAntiAffinityPingPong(t *testing.T) {
	s, ctx := testStore(t)
	a := labeledRunner(t, ctx, s, "pp-a", []string{"linux"}, "docker")
	b := labeledRunner(t, ctx, s, "pp-b", []string{"linux"}, "docker")

	pid, wid, vid := fixture(t, ctx, s)
	jobs := queueJob(t, ctx, s, pid, wid, vid, "trusted", false)

	// Attempt 1 on a → requeue → attempt 2 on b → requeue.
	if c, err := s.ClaimJob(ctx, a.ID, 30*time.Second); err != nil || c == nil {
		t.Fatalf("claim a: claim=%v err=%v", c, err)
	}
	if _, err := s.RequeueJob(ctx, a.ID, jobs[0].ID, "t"); err != nil {
		t.Fatalf("requeue a: %v", err)
	}
	if c, err := s.ClaimJob(ctx, b.ID, 30*time.Second); err != nil || c == nil {
		t.Fatalf("claim b: claim=%v err=%v", c, err)
	}
	if _, err := s.RequeueJob(ctx, b.ID, jobs[0].ID, "t"); err != nil {
		t.Fatalf("requeue b: %v", err)
	}

	// Last attempt ran on b, so a is not penalized and must claim.
	c, err := s.ClaimJob(ctx, a.ID, 30*time.Second)
	if err != nil {
		t.Fatalf("claim a2: %v", err)
	}
	if c == nil || c.Job.ID != jobs[0].ID {
		t.Fatalf("ping-pong starved the job: %+v", c)
	}
}
