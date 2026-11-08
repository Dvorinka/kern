package orchestrator

// Dispatch-inputs tests hit PostgreSQL like the rest of the advance suite:
// KERN_TEST_DATABASE_URL must name a throwaway database containing kern_test.

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/dvorinka/kern/internal/store"
)

const dispatchInputsWf = `name: IT
on: workflow_dispatch
jobs:
  build:
    runs-on: ${{ inputs.runner }}
    steps:
      - run: echo hi
`

func dispatchFixture(t *testing.T, ctx context.Context, s *store.Store) store.CreateRunParams {
	t.Helper()
	org, err := s.FirstOrg(ctx)
	if err != nil {
		t.Fatalf("org: %v", err)
	}
	p, err := s.CreateProject(ctx, org.ID, "it-"+uuid.NewString()[:8], "it", "", "it/repo", "", "main")
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	wf, err := s.UpsertWorkflow(ctx, p.ID, ".github/workflows/it.yml", "IT")
	if err != nil {
		t.Fatalf("workflow: %v", err)
	}
	v, err := s.PutWorkflowVersion(ctx, wf.ID, "deadbeef", dispatchInputsWf, map[string]any{}, true, nil, nil)
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	return params(p.ID, wf.ID, v.ID, "refs/heads/main")
}

func TestDispatchInputsResolveRunsOn(t *testing.T) {
	s, ctx := testStore(t)
	var reported []string
	o := newOrch(s, &reported)

	p := dispatchFixture(t, ctx, s)
	p.Inputs = map[string]any{"runner": "hp-win11"}
	run, err := o.StartRun(ctx, p)
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	job := jobByKey(t, ctx, s, run.ID, "build")
	if len(job.RunsOn) != 1 || job.RunsOn[0] != "hp-win11" {
		t.Fatalf("runs_on = %v, want [hp-win11]", job.RunsOn)
	}
	inputs, err := job.InputsMap()
	if err != nil {
		t.Fatalf("inputs map: %v", err)
	}
	if inputs["runner"] != "hp-win11" {
		t.Fatalf("inputs[runner] = %q, want hp-win11", inputs["runner"])
	}
}

func TestDispatchInputsLeaveUnresolvedLiteral(t *testing.T) {
	s, ctx := testStore(t)
	var reported []string
	o := newOrch(s, &reported)

	p := dispatchFixture(t, ctx, s)
	run, err := o.StartRun(ctx, p)
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	job := jobByKey(t, ctx, s, run.ID, "build")
	if len(job.RunsOn) != 1 || job.RunsOn[0] != "${{ inputs.runner }}" {
		t.Fatalf("runs_on = %v, want literal expr", job.RunsOn)
	}
}
