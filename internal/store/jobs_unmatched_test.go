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
