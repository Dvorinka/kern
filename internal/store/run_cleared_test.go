package store

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func setRunState(t testing.TB, ctx context.Context, s *Store, runID uuid.UUID, status, conclusion string) {
	t.Helper()
	if _, err := s.pool.Exec(ctx, `
		UPDATE workflow_runs SET status = $2, conclusion = $3, completed_at = now()
		WHERE id = $1`, runID, status, conclusion); err != nil {
		t.Fatalf("set run state: %v", err)
	}
}

func mkRun(t testing.TB, ctx context.Context, s *Store, pid, wid, vid uuid.UUID) Run {
	t.Helper()
	run, err := s.CreateRun(ctx, CreateRunParams{
		ProjectID: pid, WorkflowID: wid, WorkflowVersionID: vid,
		Event: "push", Ref: "refs/heads/main", TrustLevel: "trusted",
	}, nil)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	return run
}

func TestClearedRunsHiddenFromLists(t *testing.T) {
	s, ctx := testStore(t)
	pid, wid, vid := fixture(t, ctx, s)

	cancelled := mkRun(t, ctx, s, pid, wid, vid)
	setRunState(t, ctx, s, cancelled.ID, "cancelled", "cancelled")
	done := mkRun(t, ctx, s, pid, wid, vid)
	setRunState(t, ctx, s, done.ID, "completed", "success")

	// Before clearing: both visible, cancelled filter finds the cancelled run.
	runs, total, err := s.Runs(ctx, RunFilter{ProjectID: &pid})
	if err != nil || total != 2 || len(runs) != 2 {
		t.Fatalf("runs before clear: n=%d total=%d err=%v", len(runs), total, err)
	}
	runs, total, err = s.Runs(ctx, RunFilter{ProjectID: &pid, Status: "cancelled"})
	if err != nil || total != 1 || runs[0].ID != cancelled.ID {
		t.Fatalf("cancelled filter: n=%d total=%d err=%v", len(runs), total, err)
	}

	ok, err := s.ClearRun(ctx, cancelled.ID)
	if err != nil || !ok {
		t.Fatalf("ClearRun cancelled: ok=%v err=%v", ok, err)
	}
	// Non-cancelled runs refuse to clear.
	if ok, err := s.ClearRun(ctx, done.ID); err != nil || ok {
		t.Fatalf("ClearRun completed: ok=%v err=%v", ok, err)
	}

	runs, total, err = s.Runs(ctx, RunFilter{ProjectID: &pid})
	if err != nil || total != 1 || runs[0].ID != done.ID {
		t.Fatalf("runs after clear: n=%d total=%d err=%v", len(runs), total, err)
	}
	runs, total, err = s.Runs(ctx, RunFilter{ProjectID: &pid, IncludeCleared: true})
	if err != nil || total != 2 {
		t.Fatalf("include cleared: n=%d total=%d err=%v", len(runs), total, err)
	}
	cr, err := s.Run(ctx, cancelled.ID)
	if err != nil || cr.ClearedAt == nil {
		t.Fatalf("cleared_at not set: %+v err=%v", cr, err)
	}
}

func TestRunFiltersAndCounts(t *testing.T) {
	s, ctx := testStore(t)
	pid, wid, vid := fixture(t, ctx, s)

	queued := mkRun(t, ctx, s, pid, wid, vid) // status queued
	live := mkRun(t, ctx, s, pid, wid, vid)
	setRunState(t, ctx, s, live.ID, "in_progress", "")
	failed := mkRun(t, ctx, s, pid, wid, vid)
	setRunState(t, ctx, s, failed.ID, "completed", "failure")
	cancelled := mkRun(t, ctx, s, pid, wid, vid)
	setRunState(t, ctx, s, cancelled.ID, "cancelled", "cancelled")

	runs, total, err := s.Runs(ctx, RunFilter{ProjectID: &pid, Status: "queued,in_progress"})
	if err != nil || total != 2 {
		t.Fatalf("comma status: n=%d total=%d err=%v", len(runs), total, err)
	}
	runs, total, err = s.Runs(ctx, RunFilter{ProjectID: &pid, Conclusion: "failure"})
	if err != nil || total != 1 || runs[0].ID != failed.ID {
		t.Fatalf("conclusion filter: n=%d total=%d err=%v", len(runs), total, err)
	}
	_ = queued

	counts, err := s.RunCounts(ctx, &pid, false)
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if counts["all"] != 4 || counts["running"] != 2 || counts["failed"] != 1 || counts["cancelled"] != 1 {
		t.Fatalf("counts = %v", counts)
	}
}

func TestRunClassificationFilter(t *testing.T) {
	s, ctx := testStore(t)
	pid, wid, vid := fixture(t, ctx, s)

	mk := func(class string) Run {
		run, err := s.CreateRun(ctx, CreateRunParams{
			ProjectID: pid, WorkflowID: wid, WorkflowVersionID: vid,
			Event: "push", Ref: "refs/heads/main", TrustLevel: "trusted",
		}, []JobSeed{{Key: "build", JobKey: "build", Name: "Build", Needs: []string{}, RunsOn: []string{"x"}}})
		if err != nil {
			t.Fatalf("create run: %v", err)
		}
		if _, err := s.pool.Exec(ctx, `UPDATE jobs SET classification = $2 WHERE run_id = $1`, run.ID, class); err != nil {
			t.Fatal(err)
		}
		return run
	}
	oom := mk("oom")
	mk("network")
	mk("")

	runs, total, err := s.Runs(ctx, RunFilter{ProjectID: &pid, Classification: "oom"})
	if err != nil || total != 1 || len(runs) != 1 || runs[0].ID != oom.ID {
		t.Fatalf("classification filter: n=%d total=%d err=%v", len(runs), total, err)
	}
	if _, total, _ := s.Runs(ctx, RunFilter{ProjectID: &pid, Classification: "hang"}); total != 0 {
		t.Fatalf("hang filter should match nothing, total=%d", total)
	}
	if _, total, _ := s.Runs(ctx, RunFilter{ProjectID: &pid}); total != 3 {
		t.Fatalf("unfiltered total=%d, want 3", total)
	}
}

func TestClearCancelledRunsScoped(t *testing.T) {
	s, ctx := testStore(t)
	pid, wid, vid := fixture(t, ctx, s)
	org, err := s.FirstOrg(ctx)
	if err != nil {
		t.Fatal(err)
	}

	c1 := mkRun(t, ctx, s, pid, wid, vid)
	setRunState(t, ctx, s, c1.ID, "cancelled", "cancelled")
	c2 := mkRun(t, ctx, s, pid, wid, vid)
	setRunState(t, ctx, s, c2.ID, "cancelled", "cancelled")
	done := mkRun(t, ctx, s, pid, wid, vid)
	setRunState(t, ctx, s, done.ID, "completed", "success")

	n, err := s.ClearCancelledRuns(ctx, org.ID, nil)
	if err != nil || n != 2 {
		t.Fatalf("clear all: n=%d err=%v", n, err)
	}
	if _, total, _ := s.Runs(ctx, RunFilter{ProjectID: &pid}); total != 1 {
		t.Fatalf("after bulk clear total=%d, want 1", total)
	}

	// Second project: project scoping must not leak across projects.
	pid2, wid2, vid2 := fixture(t, ctx, s)
	c3 := mkRun(t, ctx, s, pid2, wid2, vid2)
	setRunState(t, ctx, s, c3.ID, "cancelled", "cancelled")
	n, err = s.ClearCancelledRuns(ctx, org.ID, &pid)
	if err != nil || n != 0 {
		t.Fatalf("clear scoped to other project: n=%d err=%v", n, err)
	}
	n, err = s.ClearCancelledRuns(ctx, org.ID, &pid2)
	if err != nil || n != 1 {
		t.Fatalf("clear scoped to pid2: n=%d err=%v", n, err)
	}
}
