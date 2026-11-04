package api

import (
	"testing"

	"github.com/dvorinka/kern/internal/workflows"
)

func prPlan(events []string, trigger map[string]any) *workflows.Plan {
	return &workflows.Plan{
		Events:   events,
		Triggers: map[string]any{"pull_request": trigger},
	}
}

func TestPullRequestMatchesPlan(t *testing.T) {
	cases := []struct {
		name       string
		plan       *workflows.Plan
		action     string
		baseBranch string
		want       bool
	}{
		{"subscribed default action", prPlan([]string{"pull_request"}, nil), "opened", "main", true},
		{"synchronize default", prPlan([]string{"pull_request"}, nil), "synchronize", "main", true},
		{"closed not a default trigger", prPlan([]string{"pull_request"}, nil), "closed", "main", false},
		{"not subscribed", prPlan([]string{"push"}, nil), "opened", "main", false},
		{"types filter hit", prPlan([]string{"pull_request"}, map[string]any{"types": []any{"labeled"}}), "labeled", "main", true},
		{"types filter miss", prPlan([]string{"pull_request"}, map[string]any{"types": []any{"labeled"}}), "opened", "main", false},
		{"branches match", prPlan([]string{"pull_request"}, map[string]any{"branches": []any{"main"}}), "opened", "main", true},
		{"branches miss", prPlan([]string{"pull_request"}, map[string]any{"branches": []any{"main"}}), "opened", "dev", false},
		{"branches-ignore hit", prPlan([]string{"pull_request"}, map[string]any{"branches-ignore": []any{"release/*"}}), "opened", "release/1.0", false},
		{"unsupported filter declared", prPlan([]string{"pull_request"}, map[string]any{"paths": []any{"src/**"}}), "opened", "main", false},
	}
	for _, tc := range cases {
		got, _ := pullRequestMatchesPlan(tc.plan, tc.action, tc.baseBranch)
		if got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestParsePRTriggerTrust(t *testing.T) {
	body := []byte(`{
		"action": "opened", "number": 7,
		"pull_request": {
			"title": "feat: things",
			"head": {"sha": "abc123", "ref": "feat-x",
				"repo": {"full_name": "o/r", "clone_url": "https://github.com/o/r.git", "fork": false}},
			"base": {"ref": "main"}
		},
		"sender": {"login": "dev"},
		"repository": {"full_name": "o/r", "clone_url": "https://github.com/o/r.git"}
	}`)
	tr, status, _ := parsePRTrigger(body)
	if tr == nil {
		t.Fatalf("same-repo PR rejected: %s", status)
	}
	if tr.trust != "pull_request" {
		t.Fatalf("trust = %q, want pull_request", tr.trust)
	}
	if tr.ref != "refs/pull/7/head" || tr.sha != "abc123" {
		t.Fatalf("ref/sha = %q %q", tr.ref, tr.sha)
	}
	if tr.cloneURL != "https://github.com/o/r.git" {
		t.Fatalf("cloneURL = %q", tr.cloneURL)
	}
}

func TestParsePRTriggerFork(t *testing.T) {
	body := []byte(`{
		"action": "synchronize", "number": 9,
		"pull_request": {
			"title": "external change",
			"head": {"sha": "def456", "ref": "patch-1",
				"repo": {"full_name": "stranger/r", "clone_url": "https://github.com/stranger/r.git", "fork": true}},
			"base": {"ref": "main"}
		},
		"sender": {"login": "stranger"},
		"repository": {"full_name": "o/r", "clone_url": "https://github.com/o/r.git"}
	}`)
	tr, _, _ := parsePRTrigger(body)
	if tr == nil {
		t.Fatal("fork PR rejected")
	}
	if tr.trust != "fork" {
		t.Fatalf("trust = %q, want fork", tr.trust)
	}
	if tr.cloneURL != "https://github.com/stranger/r.git" {
		t.Fatalf("fork checkout must clone the head repo, got %q", tr.cloneURL)
	}
	if tr.repoName != "o/r" {
		t.Fatalf("statuses must post to base repo, got %q", tr.repoName)
	}
}

func TestParsePRTriggerMissingHead(t *testing.T) {
	body := []byte(`{
		"action": "closed", "number": 3,
		"pull_request": {"title": "x",
			"head": {"sha": "", "ref": "gone", "repo": {"full_name": "", "fork": true}},
			"base": {"ref": "main"}},
		"sender": {"login": "dev"},
		"repository": {"full_name": "o/r", "clone_url": "u"}
	}`)
	tr, status, _ := parsePRTrigger(body)
	if tr != nil || status != "ignored" {
		t.Fatalf("deleted-fork PR should be ignored, got %v %q", tr, status)
	}
}
