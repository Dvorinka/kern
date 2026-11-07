package api

import (
	"encoding/json"
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
		got, _ := pullRequestMatchesPlan(tc.plan, "pull_request", tc.action, tc.baseBranch)
		if got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestDeliveryEventsSynthesizesPullRequestTarget(t *testing.T) {
	got := deliveryEvents("pull_request")
	if len(got) != 2 || got[0] != "pull_request" || got[1] != "pull_request_target" {
		t.Fatalf("pull_request delivery events = %v", got)
	}
	if ev := deliveryEvents("pull_request_target"); len(ev) != 1 || ev[0] != "pull_request_target" {
		t.Fatalf("named target delivery must not be doubled: %v", ev)
	}
	if ev := deliveryEvents("push"); len(ev) != 1 || ev[0] != "push" {
		t.Fatalf("push = %v", ev)
	}
}

func TestMergeWebhookStatus(t *testing.T) {
	if mergeWebhookStatus("processed", "ignored") != "processed" {
		t.Fatal("a started run must win over an ignored sibling")
	}
	if mergeWebhookStatus("ignored", "processed") != "processed" {
		t.Fatal("target runs must surface when the pull_request pass starts nothing")
	}
	if mergeWebhookStatus("processed", "failed") != "failed" {
		t.Fatal("a failed pass must not be hidden")
	}
	if mergeWebhookStatus("ignored", "ignored") != "ignored" {
		t.Fatal("two ignores stay ignored")
	}
}

func TestPullRequestTargetMatchesOnlyItsOwnEvent(t *testing.T) {
	plan := &workflows.Plan{
		Events:   []string{"pull_request_target"},
		Triggers: map[string]any{"pull_request_target": nil},
	}
	if ok, _ := pullRequestMatchesPlan(plan, "pull_request_target", "opened", "main"); !ok {
		t.Fatal("pull_request_target plan should match its own event")
	}
	if ok, _ := pullRequestMatchesPlan(plan, "pull_request", "opened", "main"); ok {
		t.Fatal("pull_request_target plan must not match pull_request deliveries")
	}
	prOnly := prPlan([]string{"pull_request"}, nil)
	if ok, _ := pullRequestMatchesPlan(prOnly, "pull_request_target", "opened", "main"); ok {
		t.Fatal("pull_request plan must not match pull_request_target deliveries")
	}
}

func TestParsePRTriggerTrust(t *testing.T) {
	body := []byte(`{
		"action": "opened", "number": 7,
		"pull_request": {
			"title": "feat: things",
			"author_association": "CONTRIBUTOR",
			"head": {"sha": "abc123", "ref": "feat-x",
				"repo": {"full_name": "o/r", "clone_url": "https://github.com/o/r.git", "fork": false}},
			"base": {"ref": "main", "sha": "base999"}
		},
		"sender": {"login": "dev"},
		"repository": {"full_name": "o/r", "clone_url": "https://github.com/o/r.git", "default_branch": "main"}
	}`)
	tr, status, _ := parsePRTrigger(body, "pull_request")
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
	if tr.association != "CONTRIBUTOR" || tr.headRef != "feat-x" {
		t.Fatalf("association/headRef = %q %q", tr.association, tr.headRef)
	}
}

func TestParsePRTriggerFork(t *testing.T) {
	body := []byte(`{
		"action": "synchronize", "number": 9,
		"pull_request": {
			"title": "external change",
			"author_association": "FIRST_TIME_CONTRIBUTOR",
			"head": {"sha": "def456", "ref": "patch-1",
				"repo": {"full_name": "stranger/r", "clone_url": "https://github.com/stranger/r.git", "fork": true}},
			"base": {"ref": "main", "sha": "base999"}
		},
		"sender": {"login": "stranger"},
		"repository": {"full_name": "o/r", "clone_url": "https://github.com/o/r.git", "default_branch": "main"}
	}`)
	tr, _, _ := parsePRTrigger(body, "pull_request")
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
	if tr.association != "FIRST_TIME_CONTRIBUTOR" {
		t.Fatalf("association = %q", tr.association)
	}
}

func TestParsePRTriggerMissingHead(t *testing.T) {
	body := []byte(`{
		"action": "closed", "number": 3,
		"pull_request": {"title": "x",
			"head": {"sha": "", "ref": "gone", "repo": {"full_name": "", "fork": true}},
			"base": {"ref": "main"}},
		"sender": {"login": "dev"},
		"repository": {"full_name": "o/r", "clone_url": "u", "default_branch": "main"}
	}`)
	tr, status, _ := parsePRTrigger(body, "pull_request")
	if tr != nil || status != "ignored" {
		t.Fatalf("deleted-fork PR should be ignored, got %v %q", tr, status)
	}
}

func TestParsePRTargetTrigger(t *testing.T) {
	body := []byte(`{
		"action": "opened", "number": 11,
		"pull_request": {
			"title": "external change",
			"author_association": "NONE",
			"head": {"sha": "def456", "ref": "patch-1",
				"repo": {"full_name": "stranger/r", "clone_url": "https://github.com/stranger/r.git", "fork": true}},
			"base": {"ref": "main", "sha": "base999"}
		},
		"sender": {"login": "stranger"},
		"repository": {"full_name": "o/r", "clone_url": "https://github.com/o/r.git", "default_branch": "trunk"}
	}`)
	tr, status, _ := parsePRTrigger(body, "pull_request_target")
	if tr == nil {
		t.Fatalf("pull_request_target rejected: %s", status)
	}
	// Base-repo context: trusted (secrets allowed), default-branch clone.
	if tr.trust != "trusted" {
		t.Fatalf("trust = %q, want trusted", tr.trust)
	}
	if tr.cloneURL != "https://github.com/o/r.git" {
		t.Fatalf("cloneURL = %q, want base repo", tr.cloneURL)
	}
	if tr.defaultBranch != "trunk" || tr.baseSHA != "base999" || tr.headRef != "patch-1" {
		t.Fatalf("target fields = %q %q %q", tr.defaultBranch, tr.baseSHA, tr.headRef)
	}
	// sha/ref stay empty until resolvePRTargetSHA pins the default head.
	if tr.sha != "" || tr.ref != "" {
		t.Fatalf("target trigger should defer sha/ref, got %q %q", tr.sha, tr.ref)
	}
}

func TestForkGateHold(t *testing.T) {
	cases := []struct {
		gate        string
		association string
		want        bool
	}{
		{"off", "NONE", false},
		{"", "FIRST_TIMER", false},
		{"all", "OWNER", true},
		{"all", "CONTRIBUTOR", true},
		{"first-time", "FIRST_TIME_CONTRIBUTOR", true},
		{"first-time", "FIRST_TIMER", true},
		{"first-time", "NONE", true},
		{"first-time", "", true},
		{"first-time", "CONTRIBUTOR", false},
		{"first-time", "MEMBER", false},
		{"first-time", "COLLABORATOR", false},
		{"first-time", "MANNEQUIN", false},
	}
	for _, tc := range cases {
		if got := forkGateHold(tc.gate, tc.association); got != tc.want {
			t.Errorf("forkGateHold(%q, %q) = %v, want %v", tc.gate, tc.association, got, tc.want)
		}
	}
}

func TestPrTrustPromoted(t *testing.T) {
	cases := []struct {
		settings string
		want     bool
	}{
		{`{}`, false},
		{`{"pr_trust":"off"}`, false},
		{`{"pr_trust":"trusted"}`, true},
		{`{"pr_trust":"bogus"}`, false},
	}
	for _, tc := range cases {
		if got := prTrustPromoted(json.RawMessage(tc.settings)); got != tc.want {
			t.Errorf("prTrustPromoted(%s) = %v, want %v", tc.settings, got, tc.want)
		}
	}
}
