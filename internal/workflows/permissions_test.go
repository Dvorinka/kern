package workflows

import "testing"

// TestEffectivePermissions verifies the GitHub semantics: a job-level
// permissions map replaces the workflow-level one outright; absent both,
// no ceiling is recorded.
func TestEffectivePermissions(t *testing.T) {
	wf := Workflow{Permissions: map[string]string{"contents": "read"}}
	job := &Job{Permissions: map[string]string{"contents": "write", "issues": "none"}}
	got := effectivePermissions(wf, job)
	if got["contents"] != "write" || got["issues"] != "none" || len(got) != 2 {
		t.Fatalf("job permissions should win outright, got %v", got)
	}
	job.Permissions = nil
	got = effectivePermissions(wf, job)
	if len(got) != 1 || got["contents"] != "read" {
		t.Fatalf("workflow permissions should apply, got %v", got)
	}
	wf.Permissions = nil
	if got := effectivePermissions(wf, job); got != nil {
		t.Fatalf("no permissions anywhere should yield nil, got %v", got)
	}
}
