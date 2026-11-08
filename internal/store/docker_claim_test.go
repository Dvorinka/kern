package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// dockerRunner registers a runner with an explicit label set and executor.
func labeledRunner(t testing.TB, ctx context.Context, s *Store, name string,
	labels []string, executor string) Runner {
	t.Helper()
	org, err := s.FirstOrg(ctx)
	if err != nil {
		t.Fatalf("org: %v", err)
	}
	r, err := s.RegisterRunner(ctx, RegisterRunnerParams{
		OrgID: org.ID, Name: name, Hostname: name, OS: "linux", Arch: "amd64",
		Labels: labels, TokenHash: "h-" + name, Executor: executor,
	})
	if err != nil {
		t.Fatalf("runner %s: %v", name, err)
	}
	return r
}

// seedDockerRun persists one queued job, optionally flagged needs_docker.
func seedDockerRun(t testing.TB, ctx context.Context, s *Store, p CreateRunParams,
	needsDocker bool, runsOn []string) []Job {
	t.Helper()
	run, err := s.CreateRun(ctx, p, []JobSeed{{
		Key: "j" + uuid.NewString()[:4], JobKey: "build", Name: "build",
		RunsOn: runsOn, Needs: []string{}, Image: "ubuntu:24.04",
		TimeoutSeconds: 3600, NeedsDocker: needsDocker,
	}})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	jobs, err := s.JobsForRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("jobs: %v", err)
	}
	for _, j := range jobs {
		if err := s.SetJobQueued(ctx, j.ID); err != nil {
			t.Fatalf("queue job: %v", err)
		}
	}
	if _, err := s.PromoteQueuedRuns(ctx); err != nil {
		t.Fatalf("promote: %v", err)
	}
	return jobs
}

// A needs_docker job on a trusted run must wait for a kern-docker-capable
// runner: the plain docker runner's claim comes back empty, the host
// executor's claim succeeds.
func TestDockerJobPrefersKernDockerRunner(t *testing.T) {
	s, ctx := testStore(t)
	host := labeledRunner(t, ctx, s, "host-docker", []string{"ubuntu-latest", "kern-docker"}, "host")
	docker := labeledRunner(t, ctx, s, "plain-docker", []string{"ubuntu-latest"}, "docker")

	pid, wid, vid := fixture(t, ctx, s)
	jobs := seedDockerRun(t, ctx, s, CreateRunParams{
		ProjectID: pid, WorkflowID: wid, WorkflowVersionID: vid,
		Event: "push", Ref: "refs/heads/main", TrustLevel: "trusted",
	}, true, []string{"ubuntu-latest"})

	if c, err := s.ClaimJob(ctx, docker.ID, 30*time.Second); err != nil || c != nil {
		t.Fatalf("plain runner claimed docker job while host capable: claim=%v err=%v", c, err)
	}
	c, err := s.ClaimJob(ctx, host.ID, 30*time.Second)
	if err != nil {
		t.Fatalf("host claim: %v", err)
	}
	if c == nil || c.Job.ID != jobs[0].ID {
		t.Fatalf("host runner did not get docker job: %+v", c)
	}
}

// With no kern-docker runner able to satisfy runs_on, the job falls back to
// ordinary runners — the docker commands fail there exactly as before, but
// the job is never stranded.
func TestDockerJobFallsBackWithoutCapableRunner(t *testing.T) {
	s, ctx := testStore(t)
	// Host runner exists but cannot satisfy runs_on (no ubuntu-latest label).
	labeledRunner(t, ctx, s, "mac-host", []string{"macos", "kern-docker"}, "host")
	docker := labeledRunner(t, ctx, s, "plain-docker", []string{"ubuntu-latest"}, "docker")

	pid, wid, vid := fixture(t, ctx, s)
	jobs := seedDockerRun(t, ctx, s, CreateRunParams{
		ProjectID: pid, WorkflowID: wid, WorkflowVersionID: vid,
		Event: "push", Ref: "refs/heads/main", TrustLevel: "trusted",
	}, true, []string{"ubuntu-latest"})

	c, err := s.ClaimJob(ctx, docker.ID, 30*time.Second)
	if err != nil {
		t.Fatalf("fallback claim: %v", err)
	}
	if c == nil || c.Job.ID != jobs[0].ID {
		t.Fatalf("docker runner did not fall back to docker job: %+v", c)
	}
}

// Untrusted runs can never use host executors, so the docker preference
// must not strand them: a fork-tier docker job stays claimable by anyone.
func TestDockerJobForkRunUnaffected(t *testing.T) {
	s, ctx := testStore(t)
	host := labeledRunner(t, ctx, s, "host-docker", []string{"ubuntu-latest", "kern-docker"}, "host")
	docker := labeledRunner(t, ctx, s, "plain-docker", []string{"ubuntu-latest"}, "docker")

	pid, wid, vid := fixture(t, ctx, s)
	jobs := seedDockerRun(t, ctx, s, CreateRunParams{
		ProjectID: pid, WorkflowID: wid, WorkflowVersionID: vid,
		Event: "pull_request", Ref: "refs/pull/1/merge", TrustLevel: "fork",
	}, true, []string{"ubuntu-latest"})

	if c, err := s.ClaimJob(ctx, host.ID, 30*time.Second); err != nil || c != nil {
		t.Fatalf("host runner claimed fork docker job: claim=%v err=%v", c, err)
	}
	c, err := s.ClaimJob(ctx, docker.ID, 30*time.Second)
	if err != nil {
		t.Fatalf("fork claim: %v", err)
	}
	if c == nil || c.Job.ID != jobs[0].ID {
		t.Fatalf("docker runner did not get fork docker job: %+v", c)
	}
}

// Jobs without docker usage are untouched by the preference.
func TestPlainJobIgnoresKernDockerPreference(t *testing.T) {
	s, ctx := testStore(t)
	labeledRunner(t, ctx, s, "host-docker", []string{"ubuntu-latest", "kern-docker"}, "host")
	docker := labeledRunner(t, ctx, s, "plain-docker", []string{"ubuntu-latest"}, "docker")

	pid, wid, vid := fixture(t, ctx, s)
	jobs := seedDockerRun(t, ctx, s, CreateRunParams{
		ProjectID: pid, WorkflowID: wid, WorkflowVersionID: vid,
		Event: "push", Ref: "refs/heads/main", TrustLevel: "trusted",
	}, false, []string{"ubuntu-latest"})

	c, err := s.ClaimJob(ctx, docker.ID, 30*time.Second)
	if err != nil {
		t.Fatalf("plain claim: %v", err)
	}
	if c == nil || c.Job.ID != jobs[0].ID {
		t.Fatalf("plain job did not go to plain runner: %+v", c)
	}
}
