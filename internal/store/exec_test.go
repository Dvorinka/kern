package store

import (
	"errors"
	"testing"
)

// Exec state machine: pending -> running -> done/failed, always under the
// job's lease — another runner may not drive the session (#132).
func TestExecRequestLifecycle(t *testing.T) {
	s, ctx := testStore(t)
	pid, wid, vid := fixture(t, ctx, s)
	_, jobs := seedRun(t, ctx, s, CreateRunParams{
		ProjectID: pid, WorkflowID: wid, WorkflowVersionID: vid,
		Event: "push", Ref: "refs/heads/main", TrustLevel: "trusted",
	}, 1)
	r1 := runner(t, ctx, s, "r1")
	r2 := runner(t, ctx, s, "r2")
	job := jobs[0]
	if _, err := s.pool.Exec(ctx,
		`UPDATE jobs SET status = 'in_progress', runner_id = $1 WHERE id = $2`, r1.ID, job.ID); err != nil {
		t.Fatalf("lease job: %v", err)
	}

	ex, err := s.CreateExec(ctx, job.ID, "cat /etc/os-release", false, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if ex.State != "pending" {
		t.Fatalf("state = %q, want pending", ex.State)
	}

	pending, err := s.PendingExecForRunner(ctx, r1.ID)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %v %v, want 1 entry", pending, err)
	}
	if dup, _ := s.PendingExecForRunner(ctx, r2.ID); len(dup) != 0 {
		t.Fatal("pending exec leaked to the wrong runner")
	}

	if _, err := s.StartExec(ctx, ex.ID, r2.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign-runner start = %v, want ErrNotFound", err)
	}
	if _, err := s.StartExec(ctx, ex.ID, r1.ID); err != nil {
		t.Fatalf("start: %v", err)
	}
	if again := func() error { _, err := s.StartExec(ctx, ex.ID, r1.ID); return err }(); !errors.Is(again, ErrNotFound) {
		t.Fatalf("second start = %v, want ErrNotFound", again)
	}

	code := 0
	if err := s.FinishExec(ctx, ex.ID, r1.ID, &code, ""); err != nil {
		t.Fatalf("finish: %v", err)
	}
	done, err := s.Exec(ctx, ex.ID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if done.State != "done" || done.ExitCode == nil || *done.ExitCode != 0 {
		t.Fatalf("finished state = %q code=%v", done.State, done.ExitCode)
	}

	// Job finish sweeps a still-live session.
	ex2, err := s.CreateExec(ctx, job.ID, "sleep 60", false, nil)
	if err != nil {
		t.Fatalf("create2: %v", err)
	}
	if _, err := s.StartExec(ctx, ex2.ID, r1.ID); err != nil {
		t.Fatalf("start2: %v", err)
	}
	if err := s.FailExecsForJob(ctx, job.ID, "job finished"); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	dead, _ := s.Exec(ctx, ex2.ID)
	if dead.State != "failed" || dead.Error != "job finished" {
		t.Fatalf("swept state = %q err=%q", dead.State, dead.Error)
	}
}
