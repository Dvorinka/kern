package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// queuePriorityRun seeds one queued job via CreateRun and returns it.
func queuePriorityRun(t testing.TB, ctx context.Context, s *Store,
	p CreateRunParams, priority int) Job {
	t.Helper()
	run, err := s.CreateRun(ctx, p, []JobSeed{{
		Key: "j" + uuid.NewString()[:4], JobKey: "build", Name: "build",
		RunsOn: []string{"self-hosted", "linux"}, Needs: []string{},
		Image: "ubuntu:24.04", TimeoutSeconds: 3600, Priority: priority,
	}})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	jobs, err := s.JobsForRun(ctx, run.ID)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs: %v %d", err, len(jobs))
	}
	return jobs[0]
}

// #334: tag pushes and release events seed jobs with a positive priority
// offset so release runs jump routine CI; an explicit workflow priority
// always wins.
func TestReleaseBoost(t *testing.T) {
	s, ctx := testStore(t)
	pid, wid, vid := fixture(t, ctx, s)
	params := func(event, ref string) CreateRunParams {
		return CreateRunParams{
			ProjectID: pid, WorkflowID: wid, WorkflowVersionID: vid,
			Event: event, Ref: ref, TrustLevel: "trusted",
		}
	}
	for _, c := range []struct {
		name      string
		event     string
		ref       string
		priority  int
		wantBoost bool
	}{
		{"tag push", "push", "refs/tags/v4.23.13", 0, true},
		{"non-v tag still boosts", "push", "refs/tags/beta-1", 0, true},
		{"release event", "release", "refs/tags/v1.0.0", 0, true},
		{"branch push", "push", "refs/heads/main", 0, false},
		{"dispatch", "workflow_dispatch", "refs/heads/main", 0, false},
		{"explicit priority wins", "push", "refs/tags/v1.0.0", 42, false},
		{"negative priority wins", "push", "refs/tags/v1.0.0", -5, false},
	} {
		j := queuePriorityRun(t, ctx, s, params(c.event, c.ref), c.priority)
		want := c.priority
		if c.wantBoost {
			want = 10
		}
		if j.Priority != want {
			t.Errorf("%s: priority = %d, want %d", c.name, j.Priority, want)
		}
	}
}

// #334 end-to-end: a boosted release job queued AFTER a routine job is
// claimed first — ORDER BY priority DESC, queued_at must hold under the
// real claim query, not just at seed time.
func TestReleaseBoostWinsClaim(t *testing.T) {
	s, ctx := testStore(t)
	r := labeledRunner(t, ctx, s, "ci", []string{"ubuntu-latest"}, "docker")
	pid, wid, vid := fixture(t, ctx, s)

	slow := seedDockerRun(t, ctx, s, CreateRunParams{
		ProjectID: pid, WorkflowID: wid, WorkflowVersionID: vid,
		Event: "push", Ref: "refs/heads/main", TrustLevel: "trusted",
	}, false, []string{"ubuntu-latest"})
	release := seedDockerRun(t, ctx, s, CreateRunParams{
		ProjectID: pid, WorkflowID: wid, WorkflowVersionID: vid,
		Event: "push", Ref: "refs/tags/v4.23.13", TrustLevel: "trusted",
	}, false, []string{"ubuntu-latest"})

	c, err := s.ClaimJob(ctx, r.ID, 30*time.Second)
	if err != nil || c == nil {
		t.Fatalf("claim: %v %v", c, err)
	}
	if c.Job.ID != release[0].ID {
		t.Fatalf("claimed %v, want boosted release job %v (routine %v)",
			c.Job.ID, release[0].ID, slow[0].ID)
	}
}
