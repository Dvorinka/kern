package orchestrator

import "testing"

func TestAutoConcurrencyScope(t *testing.T) {
	cases := []struct {
		name           string
		event, ref     string
		pr             int64
		defaultBranch  string
		wantScope      string
		wantCancel     bool
	}{
		{"pr cancels", "pull_request", "refs/pull/7/head", 7, "main", "pr:7", true},
		{"pr_target cancels", "pull_request_target", "refs/pull/7/merge", 7, "main", "pr:7", true},
		{"pr missing number", "pull_request", "refs/pull/0/head", 0, "main", "", false},
		{"push feature cancels", "push", "refs/heads/feat/x", 0, "main", "ref:refs/heads/feat/x", true},
		{"push default serializes", "push", "refs/heads/main", 0, "main", "ref:refs/heads/main", false},
		{"push default unknown branch serializes", "push", "refs/heads/main", 0, "", "ref:refs/heads/main", false},
		{"schedule no group", "schedule", "refs/heads/main", 0, "main", "", false},
		{"dispatch no group", "workflow_dispatch", "refs/heads/main", 0, "main", "", false},
		{"push empty ref", "push", "", 0, "main", "", false},
	}
	for _, tc := range cases {
		scope, cancel := autoConcurrencyScope(tc.event, tc.ref, tc.pr, tc.defaultBranch)
		if scope != tc.wantScope || cancel != tc.wantCancel {
			t.Errorf("%s: got (%q,%v) want (%q,%v)", tc.name, scope, cancel, tc.wantScope, tc.wantCancel)
		}
	}
}
