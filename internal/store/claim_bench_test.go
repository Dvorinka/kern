package store

// Scheduler-cost benchmarks. These run only against KERN_TEST_DATABASE_URL —
// same throwaway-DB guard as the integration tests, since they seed and
// mutate queue state.

import (
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
)

// BenchmarkClaimJob measures the claim statement under two regimes: an empty
// queue (the hot long-poll path, every 500ms per idle worker) and a deep
// backlog with a multi-runner fleet (the least-loaded NOT EXISTS pays per
// candidate job). Backlog claims are requeued between iterations so the queue
// depth stays constant.
func BenchmarkClaimJob(b *testing.B) {
	s, ctx := testStore(b)
	pid, wid, vid := fixture(b, ctx, s)
	params := func() CreateRunParams {
		return CreateRunParams{
			ProjectID: pid, WorkflowID: wid, WorkflowVersionID: vid,
			Event: "push", Ref: "refs/heads/main", TrustLevel: "trusted",
		}
	}

	claimer := runner(b, ctx, s, "bench-claimer")
	b.Run("empty_queue", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := s.ClaimJob(ctx, claimer.ID, time.Minute); err != nil {
				b.Fatal(err)
			}
		}
	})

	// Fleet peers share the org; the claimer stays the least-loaded runner so
	// claims keep succeeding while the NOT EXISTS still scans candidates.
	peers := []Runner{claimer}
	for i := 1; i < 5; i++ {
		peers = append(peers, runner(b, ctx, s, fmt.Sprintf("bench-peer-%d", i)))
	}
	for i := 0; i < 25; i++ {
		seedRun(b, ctx, s, params(), 8) // 200 queued jobs
	}
	// Give each peer a few in-progress jobs so per-runner counts are non-zero.
	// Direct UPDATE: claiming round-robin would just exercise the yield path.
	for _, p := range peers[1:] {
		if _, err := s.pool.Exec(ctx, `
			UPDATE jobs SET status = 'in_progress', runner_id = $1, attempt = 1,
				started_at = now(), lease_expires_at = now() + interval '1 hour'
			WHERE id IN (
				SELECT id FROM jobs WHERE status = 'queued' ORDER BY queued_at LIMIT 4)`, p.ID); err != nil {
			b.Fatal(err)
		}
	}

	b.Run("backlog_184", func(b *testing.B) {
		var last uuid.UUID
		lat := make([]time.Duration, 0, 512)
		for i := 0; i < b.N; i++ {
			b.StopTimer()
			if last != uuid.Nil {
				if _, err := s.pool.Exec(ctx, `
					UPDATE jobs SET status = 'queued', runner_id = NULL,
						started_at = NULL, lease_expires_at = NULL, queued_at = now()
					WHERE id = $1`, last); err != nil {
					b.Fatal(err)
				}
			}
			b.StartTimer()
			t0 := time.Now()
			c, err := s.ClaimJob(ctx, claimer.ID, time.Minute)
			lat = append(lat, time.Since(t0))
			if err != nil {
				b.Fatal(err)
			}
			if c == nil {
				b.Fatal("expected a claim under backlog")
			}
			last = c.Job.ID
		}
		sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
		p50 := lat[len(lat)/2]
		p95 := lat[len(lat)*95/100]
		fmt.Printf("claim backlog_184 latencies: p50=%v p95=%v max=%v n=%d\n",
			p50, p95, lat[len(lat)-1], len(lat))
	})
}

// BenchmarkJobsForRun contrasts the steps-bearing and steps-free listings on
// a run shaped like a matrix fan-out — the query AdvanceRun runs every tick.
func BenchmarkJobsForRun(b *testing.B) {
	s, ctx := testStore(b)
	pid, wid, vid := fixture(b, ctx, s)
	seeds := make([]JobSeed, 20)
	for i := range seeds {
		seeds[i] = JobSeed{
			Key: fmt.Sprintf("j%d", i), JobKey: "build", RunsOn: []string{"ubuntu-latest"},
			Needs: []string{}, Image: "ubuntu:24.04", TimeoutSeconds: 3600,
			Steps: []StepSeed{
				{Key: "1", Name: "checkout"}, {Key: "2", Name: "setup"},
				{Key: "3", Name: "build"}, {Key: "4", Name: "test"},
				{Key: "5", Name: "publish"},
			},
		}
	}
	run, err := s.CreateRun(ctx, CreateRunParams{
		ProjectID: pid, WorkflowID: wid, WorkflowVersionID: vid,
		Event: "push", Ref: "refs/heads/main", TrustLevel: "trusted",
	}, seeds)
	if err != nil {
		b.Fatal(err)
	}
	b.Run("with_steps", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := s.JobsForRun(ctx, run.ID); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("no_steps", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := s.JobsForRunNoSteps(ctx, run.ID); err != nil {
				b.Fatal(err)
			}
		}
	})
}
