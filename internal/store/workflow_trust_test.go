package store

// Trusted workflow versions (#368): only canonical default-branch content
// may be picked by LatestWorkflowVersion and the cron scheduler. Pull-request
// heads and ad-hoc dispatches keep their versions for run history but must
// never become the "latest" definition trusted runs execute.

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestWorkflowVersionTrust(t *testing.T) {
	s, ctx := testStore(t)
	pid, wid, _ := fixture(t, ctx, s)

	// An untrusted write (fork PR head, ad-hoc dispatch) lands newest but
	// must not displace the trusted fixture version.
	if _, err := s.PutWorkflowVersion(ctx, wid, "cafe01", "name: Evil", map[string]any{}, true, nil, nil, false); err != nil {
		t.Fatalf("untrusted version: %v", err)
	}
	_, latest, err := s.LatestWorkflowVersion(ctx, pid, ".github/workflows/it.yml")
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if latest.CommitSHA != "deadbeef" || !latest.Trusted {
		t.Fatalf("untrusted version became canonical: sha=%s trusted=%v", latest.CommitSHA, latest.Trusted)
	}

	// A trusted write at a newer sha becomes canonical.
	if _, err := s.PutWorkflowVersion(ctx, wid, "cafe02", "name: CI", map[string]any{}, true, nil, nil, true); err != nil {
		t.Fatalf("trusted version: %v", err)
	}
	_, latest, err = s.LatestWorkflowVersion(ctx, pid, ".github/workflows/it.yml")
	if err != nil || latest.CommitSHA != "cafe02" {
		t.Fatalf("trusted latest: sha=%s err=%v", latest.CommitSHA, err)
	}

	// A later untrusted write of the same commit cannot downgrade it —
	// trust is sticky so a PR rebuild of merged code stays canonical.
	v, err := s.PutWorkflowVersion(ctx, wid, "cafe02", "name: CI", map[string]any{}, true, nil, nil, false)
	if err != nil || !v.Trusted {
		t.Fatalf("trusted flag downgraded: trusted=%v err=%v", v.Trusted, err)
	}
}

func TestLatestWorkflowVersionTrustedOnly(t *testing.T) {
	s, ctx := testStore(t)
	pid, _, _ := fixture(t, ctx, s)
	org, err := s.FirstOrg(ctx)
	if err != nil {
		t.Fatalf("org: %v", err)
	}
	_ = org
	p2, err := s.CreateProject(ctx, org.ID, "pr-"+uuid.NewString()[:8], "pr", "", "pr/repo", "", "main")
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	wf, err := s.UpsertWorkflow(ctx, p2.ID, ".github/workflows/pr-only.yml", "PR Only")
	if err != nil {
		t.Fatalf("workflow: %v", err)
	}
	// Only an untrusted version exists — LatestWorkflowVersion must report
	// not-found rather than handing fork YAML to a trusted trigger.
	if _, err := s.PutWorkflowVersion(ctx, wf.ID, "f00ba7", "name: PR", map[string]any{}, true, nil, nil, false); err != nil {
		t.Fatalf("untrusted version: %v", err)
	}
	if _, _, err := s.LatestWorkflowVersion(ctx, p2.ID, ".github/workflows/pr-only.yml"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("latest with only untrusted versions = %v, want ErrNotFound", err)
	}
	_ = pid
}

func TestScheduledWorkflowsTrustedOnly(t *testing.T) {
	s, ctx := testStore(t)
	pid, wid, _ := fixture(t, ctx, s)
	schedule := map[string]any{
		"triggers": map[string]any{
			"schedule": []any{map[string]any{"cron": "0 * * * *"}},
		},
	}

	// Untrusted schedule version must not reach the cron picker.
	if _, err := s.PutWorkflowVersion(ctx, wid, "sched-untrusted", "name: IT", schedule, true, nil, nil, false); err != nil {
		t.Fatalf("untrusted schedule version: %v", err)
	}
	sws, err := s.ScheduledWorkflows(ctx)
	if err != nil {
		t.Fatalf("scheduled: %v", err)
	}
	for _, sw := range sws {
		if sw.WorkflowID == wid {
			t.Fatalf("untrusted version reached cron picker: %+v", sw)
		}
	}

	// The same content trusted does.
	if _, err := s.PutWorkflowVersion(ctx, wid, "sched-trusted", "name: IT", schedule, true, nil, nil, true); err != nil {
		t.Fatalf("trusted schedule version: %v", err)
	}
	sws, err = s.ScheduledWorkflows(ctx)
	if err != nil {
		t.Fatalf("scheduled: %v", err)
	}
	found := false
	for _, sw := range sws {
		if sw.WorkflowID == wid {
			found = true
		}
	}
	if !found {
		t.Fatalf("trusted schedule version missing from cron picker")
	}
	_ = pid
}
