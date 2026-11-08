package store

import (
	"context"
	"testing"
	"time"
)

// hostRunner registers a host-executor runner; dockerRunner the default.
func executorRunner(t testing.TB, ctx context.Context, s *Store, name, executor string) Runner {
	t.Helper()
	org, err := s.FirstOrg(ctx)
	if err != nil {
		t.Fatalf("org: %v", err)
	}
	r, err := s.RegisterRunner(ctx, RegisterRunnerParams{
		OrgID: org.ID, Name: name, Hostname: name, OS: "linux", Arch: "amd64",
		Labels: []string{"ubuntu-latest"}, TokenHash: "h-" + name, Executor: executor,
	})
	if err != nil {
		t.Fatalf("runner %s: %v", name, err)
	}
	return r
}

// A fork/pull_request run must never land on a host-executor runner — host
// execution has no isolation, so the claim query itself refuses the pairing.
// A trusted run remains claimable there.
func TestHostExecutorTrustGate(t *testing.T) {
	s, ctx := testStore(t)
	host := executorRunner(t, ctx, s, "host-1", "host")
	docker := executorRunner(t, ctx, s, "docker-1", "docker")

	pid, wid, vid := fixture(t, ctx, s)

	// Fork-tier run: the host runner's claim must come back empty, and the
	// same job must then be claimable by the docker runner.
	forkRun, forkJobs := seedRun(t, ctx, s, CreateRunParams{
		ProjectID: pid, WorkflowID: wid, WorkflowVersionID: vid,
		Event: "pull_request", Ref: "refs/pull/1/merge", TrustLevel: "fork",
	}, 1)
	if c, err := s.ClaimJob(ctx, host.ID, 30*time.Second); err != nil || c != nil {
		t.Fatalf("host runner claimed fork job: claim=%v err=%v", c, err)
	}
	c, err := s.ClaimJob(ctx, docker.ID, 30*time.Second)
	if err != nil {
		t.Fatalf("docker claim: %v", err)
	}
	if c == nil || c.Job.ID != forkJobs[0].ID {
		t.Fatalf("docker runner did not get fork job: %+v", c)
	}
	_ = forkRun

	// Trusted run: the host runner is eligible and must win its own claim.
	_, trustedJobs := seedRun(t, ctx, s, CreateRunParams{
		ProjectID: pid, WorkflowID: wid, WorkflowVersionID: vid,
		Event: "push", Ref: "refs/heads/main", TrustLevel: "trusted",
	}, 1)
	c, err = s.ClaimJob(ctx, host.ID, 30*time.Second)
	if err != nil {
		t.Fatalf("host claim trusted: %v", err)
	}
	if c == nil || c.Job.ID != trustedJobs[0].ID {
		t.Fatalf("host runner could not claim trusted job: %+v", c)
	}
}

// pull_request trust tier is gated the same way as fork.
func TestHostExecutorPullRequestGate(t *testing.T) {
	s, ctx := testStore(t)
	host := executorRunner(t, ctx, s, "host-pr", "host")

	pid, wid, vid := fixture(t, ctx, s)
	seedRun(t, ctx, s, CreateRunParams{
		ProjectID: pid, WorkflowID: wid, WorkflowVersionID: vid,
		Event: "pull_request", Ref: "refs/pull/2/merge", TrustLevel: "pull_request",
	}, 1)
	if c, err := s.ClaimJob(ctx, host.ID, 30*time.Second); err != nil || c != nil {
		t.Fatalf("host runner claimed pull_request job: claim=%v err=%v", c, err)
	}
}
