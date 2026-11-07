package store

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

// Concurrent uploads near the cap must never push the project over quota:
// the FOR UPDATE serialization in CreateArtifactIfQuota makes the loser
// re-read totals that include the winner's commit.
func TestArtifactQuotaConcurrent(t *testing.T) {
	s, ctx := testStore(t)
	pid, wid, vid := fixture(t, ctx, s)
	run, jobs := seedRun(t, ctx, s, CreateRunParams{
		ProjectID: pid, WorkflowID: wid, WorkflowVersionID: vid,
		Event: "push", Ref: "refs/heads/main", TrustLevel: "trusted",
	}, 1)

	const quota = int64(3 << 20) // 3 MiB
	const size = int64(1 << 20)  // 1 MiB each — 5 racers, exactly 3 may win
	const racers = 5

	var wg sync.WaitGroup
	errs := make([]error, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.CreateArtifactIfQuota(ctx, run.ID, jobs[0].ID,
				fmt.Sprintf("a%d", i), fmt.Sprintf("blob/%d", i), size, "sha", quota)
		}(i)
	}
	wg.Wait()

	ok, over := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrArtifactQuota):
			over++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 3 || over != 2 {
		t.Fatalf("quota race: %d succeeded, %d rejected — want 3/2", ok, over)
	}
	total, err := s.ArtifactBytesForProject(ctx, pid)
	if err != nil {
		t.Fatalf("total: %v", err)
	}
	if total > quota {
		t.Fatalf("project total %d exceeds quota %d", total, quota)
	}
}

// Same-name overwrite replaces bytes in the total rather than adding.
func TestArtifactQuotaOverwrite(t *testing.T) {
	s, ctx := testStore(t)
	pid, wid, vid := fixture(t, ctx, s)
	run, jobs := seedRun(t, ctx, s, CreateRunParams{
		ProjectID: pid, WorkflowID: wid, WorkflowVersionID: vid,
		Event: "push", Ref: "refs/heads/main", TrustLevel: "trusted",
	}, 1)

	const quota = int64(3 << 20)
	const size = int64(2 << 20)

	if _, err := s.CreateArtifactIfQuota(ctx, run.ID, jobs[0].ID, "a", "b1", size, "s", quota); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	// A second 2MiB under a different name would exceed 3MiB — rejected.
	if _, err := s.CreateArtifactIfQuota(ctx, run.ID, jobs[0].ID, "b", "b2", size, "s", quota); !errors.Is(err, ErrArtifactQuota) {
		t.Fatalf("expected quota rejection, got %v", err)
	}
	// Overwriting "a" with the same size replaces, not adds — still fits.
	if _, err := s.CreateArtifactIfQuota(ctx, run.ID, jobs[0].ID, "a", "b3", size, "s", quota); err != nil {
		t.Fatalf("overwrite should fit: %v", err)
	}
	total, err := s.ArtifactBytesForProject(ctx, pid)
	if err != nil {
		t.Fatalf("total: %v", err)
	}
	if total != size {
		t.Fatalf("total %d — overwrite should leave %d", total, size)
	}
}
