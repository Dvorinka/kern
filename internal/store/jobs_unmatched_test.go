package store

import (
	"testing"

	"github.com/google/uuid"
)

// A queued job whose runs-on labels no online runner covers must surface in
// UnmatchedQueuedJobs; one a runner can claim must not. Capacity is ignored —
// a busy matching runner still counts as a match.
func TestUnmatchedQueuedJobs(t *testing.T) {
	s, ctx := testStore(t)
	pid, wid, vid := fixture(t, ctx, s)
	org, _ := s.FirstOrg(ctx)

	mkJob := func(runsOn []string, trust string) Job {
		t.Helper()
		run, err := s.CreateRun(ctx, CreateRunParams{
			ProjectID: pid, WorkflowID: wid, WorkflowVersionID: vid,
			Event: "push", Ref: "refs/heads/main", TrustLevel: trust,
		}, []JobSeed{{
			Key: "j" + uuid.NewString()[:6], JobKey: "build", RunsOn: runsOn,
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
			t.Fatalf("queue job: %v", err)
		}
		return jobs[0]
	}

	starved := mkJob([]string{"self-hosted", "linux", "nonexistent-label"}, "trusted")
	mkJob([]string{"self-hosted", "linux", "ubuntu-latest"}, "trusted")

	// No runners at all: both starve.
	un, err := s.UnmatchedQueuedJobs(ctx)
	if err != nil {
		t.Fatalf("unmatched: %v", err)
	}
	if len(un) != 2 {
		t.Fatalf("want 2 unmatched, got %d: %+v", len(un), un)
	}

	// A runner covering only the matched job's labels leaves the starved one.
	if _, err := s.RegisterRunner(ctx, RegisterRunnerParams{
		OrgID: org.ID, Name: "r1", Hostname: "r1", OS: "linux", Arch: "amd64",
		Labels: []string{"self-hosted", "linux", "ubuntu-latest"}, TokenHash: "h-r1",
	}); err != nil {
		t.Fatalf("runner: %v", err)
	}
	un, err = s.UnmatchedQueuedJobs(ctx)
	if err != nil {
		t.Fatalf("unmatched: %v", err)
	}
	if len(un) != 1 || un[0].JobID != starved.ID {
		t.Fatalf("want only starved job unmatched, got %+v", un)
	}
}

// A runner whose claim gate is engaged does not count as capacity: the job
// becomes unmatched but PausedMatches phrases it as waiting on pressure.
func TestUnmatchedQueuedJobsPaused(t *testing.T) {
	s, ctx := testStore(t)
	pid, wid, vid := fixture(t, ctx, s)
	org, _ := s.FirstOrg(ctx)

	r, err := s.RegisterRunner(ctx, RegisterRunnerParams{
		OrgID: org.ID, Name: "paused-r", Hostname: "paused-r", OS: "linux", Arch: "amd64",
		Labels: []string{"self-hosted", "linux", "ubuntu-latest"}, TokenHash: "h-paused",
	})
	if err != nil {
		t.Fatalf("runner: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE runners SET claims_paused = 'disk' WHERE id = $1`, r.ID); err != nil {
		t.Fatalf("pause: %v", err)
	}

	run, err := s.CreateRun(ctx, CreateRunParams{
		ProjectID: pid, WorkflowID: wid, WorkflowVersionID: vid,
		Event: "push", Ref: "refs/heads/main", TrustLevel: "trusted",
	}, []JobSeed{{
		Key: "j1", JobKey: "build", RunsOn: []string{"self-hosted", "linux", "ubuntu-latest"},
		Needs: []string{}, Image: "ubuntu:24.04", TimeoutSeconds: 3600,
	}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	jobs, err := s.JobsForRun(ctx, run.ID)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs: %v %v", jobs, err)
	}
	if err := s.SetJobQueued(ctx, jobs[0].ID); err != nil {
		t.Fatalf("queue job: %v", err)
	}

	un, err := s.UnmatchedQueuedJobs(ctx)
	if err != nil {
		t.Fatalf("unmatched: %v", err)
	}
	if len(un) != 1 || un[0].JobID != jobs[0].ID {
		t.Fatalf("paused-only match should starve, got %+v", un)
	}
	if un[0].PausedMatches != 1 {
		t.Fatalf("PausedMatches = %d, want 1", un[0].PausedMatches)
	}

	// Gate opens: the runner is capacity again, no starvation.
	if _, err := s.pool.Exec(ctx, `UPDATE runners SET claims_paused = '' WHERE id = $1`, r.ID); err != nil {
		t.Fatalf("unpause: %v", err)
	}
	un, err = s.UnmatchedQueuedJobs(ctx)
	if err != nil {
		t.Fatalf("unmatched: %v", err)
	}
	if len(un) != 0 {
		t.Fatalf("unpaused runner should satisfy the job, got %+v", un)
	}
}
