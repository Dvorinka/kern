package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// setRunnerCPU writes the heartbeat-reported cpu_percent the yield tiebreak
// reads — there is no public setter, the column only moves via heartbeat.
func setRunnerCPU(t testing.TB, ctx context.Context, s *Store, r Runner, pct *float64) {
	t.Helper()
	if _, err := s.pool.Exec(ctx,
		`UPDATE runners SET cpu_percent = $1 WHERE id = $2`, pct, r.ID); err != nil {
		t.Fatalf("set cpu: %v", err)
	}
}

func setRunnerPaused(t testing.TB, ctx context.Context, s *Store, r Runner, reason string) {
	t.Helper()
	if _, err := s.pool.Exec(ctx,
		`UPDATE runners SET claims_paused = $1 WHERE id = $2`, reason, r.ID); err != nil {
		t.Fatalf("set claims_paused: %v", err)
	}
}

func queueJob(t testing.TB, ctx context.Context, s *Store, pid, wid, vid uuid.UUID,
	trust string, needsDocker bool) []Job {
	t.Helper()
	return seedDockerRun(t, ctx, s, CreateRunParams{
		ProjectID: pid, WorkflowID: wid, WorkflowVersionID: vid,
		Event: "push", Ref: "refs/heads/main", TrustLevel: trust,
	}, needsDocker, []string{"self-hosted", "linux"})
}

// Among equally job-loaded peers the cooler host wins the claim (#310):
// the saturated runner's claim yields, the cool runner's succeeds.
func TestClaimYieldsToCoolerPeer(t *testing.T) {
	s, ctx := testStore(t)
	hot := labeledRunner(t, ctx, s, "hot", []string{"linux"}, "docker")
	cool := labeledRunner(t, ctx, s, "cool", []string{"linux"}, "docker")
	ninety, ten := 90.0, 10.0
	setRunnerCPU(t, ctx, s, hot, &ninety)
	setRunnerCPU(t, ctx, s, cool, &ten)

	pid, wid, vid := fixture(t, ctx, s)
	jobs := queueJob(t, ctx, s, pid, wid, vid, "trusted", false)

	if c, err := s.ClaimJob(ctx, hot.ID, 30*time.Second); err != nil || c != nil {
		t.Fatalf("hot runner claimed over 80pt cooler peer: claim=%v err=%v", c, err)
	}
	c, err := s.ClaimJob(ctx, cool.ID, 30*time.Second)
	if err != nil {
		t.Fatalf("cool claim: %v", err)
	}
	if c == nil || c.Job.ID != jobs[0].ID {
		t.Fatalf("cool runner did not get yielded job: %+v", c)
	}
}

// CPU samples within the margin must not trade claims back and forth.
func TestClaimCPUTieWithinMargin(t *testing.T) {
	s, ctx := testStore(t)
	a := labeledRunner(t, ctx, s, "cpu-a", []string{"linux"}, "docker")
	b := labeledRunner(t, ctx, s, "cpu-b", []string{"linux"}, "docker")
	fifty, forty := 50.0, 40.0 // 10pt gap < 15pt margin
	setRunnerCPU(t, ctx, s, a, &fifty)
	setRunnerCPU(t, ctx, s, b, &forty)

	pid, wid, vid := fixture(t, ctx, s)
	jobs := queueJob(t, ctx, s, pid, wid, vid, "trusted", false)

	c, err := s.ClaimJob(ctx, a.ID, 30*time.Second)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if c == nil || c.Job.ID != jobs[0].ID {
		t.Fatalf("within-margin peer suppressed the claim: %+v", c)
	}
}

// Runners predating cpu_percent reporting read as saturated (COALESCE 100),
// so a reporting peer wins ties against them.
func TestClaimCPUUnknownTreatedAsBusy(t *testing.T) {
	s, ctx := testStore(t)
	reported := labeledRunner(t, ctx, s, "reported", []string{"linux"}, "docker")
	unreported := labeledRunner(t, ctx, s, "unreported", []string{"linux"}, "docker")
	cool := 5.0
	setRunnerCPU(t, ctx, s, reported, &cool)
	setRunnerCPU(t, ctx, s, unreported, nil) // explicit NULL

	pid, wid, vid := fixture(t, ctx, s)
	jobs := queueJob(t, ctx, s, pid, wid, vid, "trusted", false)

	// unreported (NULL→100) yields to reported (5): 5 < 100-15.
	if c, err := s.ClaimJob(ctx, unreported.ID, 30*time.Second); err != nil || c != nil {
		t.Fatalf("unreported runner claimed over cool reported peer: claim=%v err=%v", c, err)
	}
	c, err := s.ClaimJob(ctx, reported.ID, 30*time.Second)
	if err != nil {
		t.Fatalf("reported claim: %v", err)
	}
	if c == nil || c.Job.ID != jobs[0].ID {
		t.Fatalf("reported runner did not get yielded job: %+v", c)
	}
}

// A claim-gate-paused peer cannot take work — it must not suppress claims
// from healthy runners even while carrying fewer in-progress jobs.
func TestClaimNotBlockedByPausedPeer(t *testing.T) {
	s, ctx := testStore(t)
	busy := labeledRunner(t, ctx, s, "busy", []string{"linux"}, "docker")
	paused := labeledRunner(t, ctx, s, "paused", []string{"linux"}, "docker")

	pid, wid, vid := fixture(t, ctx, s)
	queueJob(t, ctx, s, pid, wid, vid, "trusted", false)
	if c, err := s.ClaimJob(ctx, busy.ID, 30*time.Second); err != nil || c == nil {
		t.Fatalf("setup claim: claim=%v err=%v", c, err)
	}

	setRunnerPaused(t, ctx, s, paused, "disk")
	jobs := queueJob(t, ctx, s, pid, wid, vid, "trusted", false)

	c, err := s.ClaimJob(ctx, busy.ID, 30*time.Second)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if c == nil || c.Job.ID != jobs[0].ID {
		t.Fatalf("paused peer suppressed a healthy runner's claim: %+v", c)
	}
}

// A host executor can never take fork/pull_request trust — it must not
// suppress a docker runner's claim on untrusted work.
func TestClaimNotBlockedByIneligibleHostPeer(t *testing.T) {
	s, ctx := testStore(t)
	labeledRunner(t, ctx, s, "host-exec", []string{"linux"}, "host")
	docker := labeledRunner(t, ctx, s, "docker-exec", []string{"linux"}, "docker")

	pid, wid, vid := fixture(t, ctx, s)
	// Give the docker runner load=1 with a trusted job the host could also
	// legally take — proving the fork exclusion is trust-scoped, not blanket.
	queueJob(t, ctx, s, pid, wid, vid, "trusted", false)
	if c, err := s.ClaimJob(ctx, docker.ID, 30*time.Second); err != nil || c == nil {
		t.Fatalf("setup claim: claim=%v err=%v", c, err)
	}

	jobs := seedDockerRun(t, ctx, s, CreateRunParams{
		ProjectID: pid, WorkflowID: wid, WorkflowVersionID: vid,
		Event: "pull_request", Ref: "refs/pull/1/merge", TrustLevel: "fork",
	}, false, []string{"self-hosted", "linux"})

	c, err := s.ClaimJob(ctx, docker.ID, 30*time.Second)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if c == nil || c.Job.ID != jobs[0].ID {
		t.Fatalf("ineligible host peer suppressed fork claim: %+v", c)
	}
}

// A non-docker peer cannot take a needs_docker job while a capable runner
// exists — it must not suppress the capable runner's claim either.
func TestClaimNotBlockedByNonDockerPeer(t *testing.T) {
	s, ctx := testStore(t)
	capable := labeledRunner(t, ctx, s, "capable", []string{"linux", "kern-docker"}, "docker")
	labeledRunner(t, ctx, s, "plain", []string{"linux"}, "docker")

	pid, wid, vid := fixture(t, ctx, s)
	queueJob(t, ctx, s, pid, wid, vid, "trusted", false)
	if c, err := s.ClaimJob(ctx, capable.ID, 30*time.Second); err != nil || c == nil {
		t.Fatalf("setup claim: claim=%v err=%v", c, err)
	}

	jobs := queueJob(t, ctx, s, pid, wid, vid, "trusted", true)

	c, err := s.ClaimJob(ctx, capable.ID, 30*time.Second)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if c == nil || c.Job.ID != jobs[0].ID {
		t.Fatalf("non-docker peer suppressed docker job claim: %+v", c)
	}
}

// The plain job-count yield still works: at equal CPU an idle peer wins
// over a loaded one, and the loaded runner yields the next claim.
func TestClaimStillYieldsOnJobCount(t *testing.T) {
	s, ctx := testStore(t)
	loaded := labeledRunner(t, ctx, s, "loaded", []string{"linux"}, "docker")
	idle := labeledRunner(t, ctx, s, "idle-peer", []string{"linux"}, "docker")

	pid, wid, vid := fixture(t, ctx, s)
	queueJob(t, ctx, s, pid, wid, vid, "trusted", false)
	if c, err := s.ClaimJob(ctx, idle.ID, 30*time.Second); err != nil || c == nil {
		t.Fatalf("idle setup claim: claim=%v err=%v", c, err)
	}
	// idle=1 job. loaded claims the next job freely (0 < 1 does not yield).
	queueJob(t, ctx, s, pid, wid, vid, "trusted", false)
	if c, err := s.ClaimJob(ctx, loaded.ID, 30*time.Second); err != nil || c == nil {
		t.Fatalf("loaded claim: claim=%v err=%v", c, err)
	}
	// Equal job counts and unreported CPU on both → no yield; loaded takes
	// a second job while idle stays at 1.
	queueJob(t, ctx, s, pid, wid, vid, "trusted", false)
	if c, err := s.ClaimJob(ctx, loaded.ID, 30*time.Second); err != nil || c == nil {
		t.Fatalf("loaded second claim: claim=%v err=%v", c, err)
	}
	third := queueJob(t, ctx, s, pid, wid, vid, "trusted", false)
	if c, err := s.ClaimJob(ctx, loaded.ID, 30*time.Second); err != nil || c != nil {
		t.Fatalf("loaded runner claimed at 2 jobs over idle peer at 1: claim=%v err=%v", c, err)
	}
	c, err := s.ClaimJob(ctx, idle.ID, 30*time.Second)
	if err != nil {
		t.Fatalf("idle claim: %v", err)
	}
	if c == nil || c.Job.ID != third[0].ID {
		t.Fatalf("idle peer did not get yielded job: %+v", c)
	}
}
