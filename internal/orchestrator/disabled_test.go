package orchestrator

// #289: a manual trigger of a disabled workflow must refuse with a typed
// error handlers can map to a 4xx — not the generic 500 that read as a
// seed-path crash.

import (
	"errors"
	"testing"

	"github.com/dvorinka/kern/internal/store"
	"github.com/dvorinka/kern/internal/workflows"
	"go.uber.org/zap"
)

func TestStartRunDisabledWorkflow(t *testing.T) {
	s, ctx := testStore(t)
	pid, wid, vid := fixture(t, ctx, s)
	if err := s.SetWorkflowDisabled(ctx, wid, true); err != nil {
		t.Fatalf("disable: %v", err)
	}
	o := New(s, zap.NewNop(), workflows.Compiler{})
	_, err := o.StartRun(ctx, store.CreateRunParams{
		ProjectID: pid, WorkflowID: wid, WorkflowVersionID: vid,
		Event: "workflow_dispatch", Ref: "refs/heads/master", TrustLevel: "trusted",
	})
	if !errors.Is(err, ErrWorkflowDisabled) {
		t.Fatalf("StartRun = %v, want ErrWorkflowDisabled", err)
	}
}
