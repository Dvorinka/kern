package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dvorinka/kern/internal/workflows"
)

func prPlan(events []string, trigger map[string]any) *workflows.Plan {
	return &workflows.Plan{
		Events:   events,
		Triggers: map[string]any{"pull_request": trigger},
	}
}

func pushPlan(events []string, trigger map[string]any) *workflows.Plan {
	return &workflows.Plan{
		Events:   events,
		Triggers: map[string]any{"push": trigger},
	}
}

func diffFiles(files ...string) func(context.Context) ([]string, error) {
	return func(context.Context) ([]string, error) { return files, nil }
}

var diffBoom = func(context.Context) ([]string, error) {
	return nil, errors.New("compare api down")
}

func TestPushMatchesPlan(t *testing.T) {
	cases := []struct {
		name  string
		plan  *workflows.Plan
		ref   string
		want  bool
		whyRe string
	}{
		// No filters: branches and tags both trigger, like GitHub.
		{"unfiltered branch", pushPlan([]string{"push"}, nil), "refs/heads/main", true, ""},
		{"unfiltered tag", pushPlan([]string{"push"}, nil), "refs/tags/v1.2.3", true, ""},
		{"unfiltered sequence form", &workflows.Plan{Events: []string{"push", "pull_request"}}, "refs/tags/v1", true, ""},
		{"not subscribed", pushPlan([]string{"pull_request"}, nil), "refs/heads/main", false, ""},

		// Branch family.
		{"branches hit", pushPlan([]string{"push"}, map[string]any{"branches": []any{"main"}}), "refs/heads/main", true, ""},
		{"branches miss", pushPlan([]string{"push"}, map[string]any{"branches": []any{"main"}}), "refs/heads/dev", false, "not in branches filter"},
		{"branches glob", pushPlan([]string{"push"}, map[string]any{"branches": []any{"release/**"}}), "refs/heads/release/1.0", true, ""},
		{"branches-ignore miss", pushPlan([]string{"push"}, map[string]any{"branches-ignore": []any{"dev"}}), "refs/heads/dev", false, "is ignored"},
		{"branches-ignore pass", pushPlan([]string{"push"}, map[string]any{"branches-ignore": []any{"dev"}}), "refs/heads/main", true, ""},

		// Tag family.
		{"tags hit", pushPlan([]string{"push"}, map[string]any{"tags": []any{"v*"}}), "refs/tags/v1.2.3", true, ""},
		{"tags semver miss", pushPlan([]string{"push"}, map[string]any{"tags": []any{"v*.*.*"}}), "refs/tags/v1", false, "not in tags filter"},
		{"tags miss", pushPlan([]string{"push"}, map[string]any{"tags": []any{"v*"}}), "refs/tags/beta", false, "not in tags filter"},
		{"tags-ignore hit", pushPlan([]string{"push"}, map[string]any{"tags-ignore": []any{"draft-*"}}), "refs/tags/draft-1", false, "is ignored"},
		{"tags-ignore pass", pushPlan([]string{"push"}, map[string]any{"tags-ignore": []any{"draft-*"}}), "refs/tags/v1.0.0", true, ""},
		{"tags negation", pushPlan([]string{"push"}, map[string]any{"tags": []any{"v*", "!v1.0.0"}}), "refs/tags/v1.0.0", false, "not in tags filter"},
		{"tags negation pass", pushPlan([]string{"push"}, map[string]any{"tags": []any{"v*", "!v1.0.0"}}), "refs/tags/v2.0.0", true, ""},

		// Ref-type scoping: a family defined for one ref type gates out the other.
		{"branches-only skips tag", pushPlan([]string{"push"}, map[string]any{"branches": []any{"main"}}), "refs/tags/v1.0.0", false, "only branches filters"},
		{"branches-ignore-only skips tag", pushPlan([]string{"push"}, map[string]any{"branches-ignore": []any{"dev"}}), "refs/tags/v1.0.0", false, "only branches filters"},
		{"tags-only skips branch", pushPlan([]string{"push"}, map[string]any{"tags": []any{"v*"}}), "refs/heads/main", false, "only tags filters"},
		{"tags-ignore-only skips branch", pushPlan([]string{"push"}, map[string]any{"tags-ignore": []any{"v*"}}), "refs/heads/main", false, "only tags filters"},

		// Both families: each ref type evaluates its own.
		{"both families tag hit", pushPlan([]string{"push"}, map[string]any{"branches": []any{"main"}, "tags": []any{"v*"}}), "refs/tags/v1.0.0", true, ""},
		{"both families tag miss", pushPlan([]string{"push"}, map[string]any{"branches": []any{"main"}, "tags": []any{"v*"}}), "refs/tags/beta", false, "not in tags filter"},
		{"both families branch hit", pushPlan([]string{"push"}, map[string]any{"branches": []any{"main"}, "tags": []any{"v*"}}), "refs/heads/main", true, ""},
		{"both families branch miss", pushPlan([]string{"push"}, map[string]any{"branches": []any{"main"}, "tags": []any{"v*"}}), "refs/heads/dev", false, "not in branches filter"},
		{"branches plus tags-ignore", pushPlan([]string{"push"}, map[string]any{"branches": []any{"main"}, "tags-ignore": []any{"v*"}}), "refs/tags/v1", false, "is ignored"},
		{"branches plus tags-ignore pass", pushPlan([]string{"push"}, map[string]any{"branches": []any{"main"}, "tags-ignore": []any{"v*"}}), "refs/tags/rc1", true, ""},

		// Empty lists count as undefined.
		{"empty tags falls back to branches gate", pushPlan([]string{"push"}, map[string]any{"branches": []any{"main"}, "tags": []any{}}), "refs/tags/v1", false, "only branches filters"},

		// Non-branch/tag refs only run unfiltered workflows.
		{"odd ref unfiltered", pushPlan([]string{"push"}, nil), "refs/pull/1/head", true, ""},
		{"odd ref filtered", pushPlan([]string{"push"}, map[string]any{"branches": []any{"main"}}), "refs/pull/1/head", false, "neither a branch nor a tag"},

		// Declared gaps still skip safely.
		{"types under push unsupported", pushPlan([]string{"push"}, map[string]any{"types": []any{"x"}}), "refs/heads/main", false, "unsupported push filter types"},
	}
	for _, tc := range cases {
		got, why := pushMatchesPlan(context.Background(), tc.plan, tc.ref, nil)
		if got != tc.want {
			t.Errorf("%s: got %v want %v (why %q)", tc.name, got, tc.want, why)
			continue
		}
		if tc.whyRe != "" && !strings.Contains(why, tc.whyRe) {
			t.Errorf("%s: why %q missing %q", tc.name, why, tc.whyRe)
		}
	}
}

func TestPushMatchesPlanPaths(t *testing.T) {
	cases := []struct {
		name  string
		plan  *workflows.Plan
		ref   string
		diff  func(context.Context) ([]string, error)
		want  bool
		whyRe string
	}{
		{"paths hit", pushPlan([]string{"push"}, map[string]any{"paths": []any{"src/**"}}), "refs/heads/main", diffFiles("src/a.go"), true, ""},
		{"paths miss", pushPlan([]string{"push"}, map[string]any{"paths": []any{"src/**"}}), "refs/heads/main", diffFiles("docs/a.md"), false, "no changed file matches paths filter"},
		{"paths empty diff", pushPlan([]string{"push"}, map[string]any{"paths": []any{"src/**"}}), "refs/heads/main", diffFiles(), false, "no changed file"},
		{"paths star no slash", pushPlan([]string{"push"}, map[string]any{"paths": []any{"docs/*"}}), "refs/heads/main", diffFiles("docs/a/b.md"), false, "no changed file"},
		{"paths star hit", pushPlan([]string{"push"}, map[string]any{"paths": []any{"docs/*"}}), "refs/heads/main", diffFiles("docs/a.md"), true, ""},
		{"paths double-star", pushPlan([]string{"push"}, map[string]any{"paths": []any{"**.go"}}), "refs/heads/main", diffFiles("a/b/c.go"), true, ""},
		{"paths double-star-dir root", pushPlan([]string{"push"}, map[string]any{"paths": []any{"**/docs/**"}}), "refs/heads/main", diffFiles("docs/x.md"), true, ""},
		{"paths double-star-dir nested", pushPlan([]string{"push"}, map[string]any{"paths": []any{"**/docs/**"}}), "refs/heads/main", diffFiles("a/b/docs/x.md"), true, ""},
		{"paths-ignore hit", pushPlan([]string{"push"}, map[string]any{"paths-ignore": []any{"src/**"}}), "refs/heads/main", diffFiles("src/a.go"), false, "matches paths-ignore"},
		{"paths-ignore miss", pushPlan([]string{"push"}, map[string]any{"paths-ignore": []any{"src/**"}}), "refs/heads/main", diffFiles("docs/a.md"), true, ""},
		{"paths plus ignore blocked", pushPlan([]string{"push"}, map[string]any{"paths": []any{"**"}, "paths-ignore": []any{"**_test.go"}}), "refs/heads/main", diffFiles("x_test.go"), false, "matches paths-ignore"},
		{"paths plus ignore pass", pushPlan([]string{"push"}, map[string]any{"paths": []any{"**"}, "paths-ignore": []any{"**_test.go"}}), "refs/heads/main", diffFiles("x.go"), true, ""},
		{"paths negation re-include", pushPlan([]string{"push"}, map[string]any{"paths": []any{"**", "!docs/**", "docs/keep.md"}}), "refs/heads/main", diffFiles("docs/keep.md"), true, ""},
		{"paths negation exclude", pushPlan([]string{"push"}, map[string]any{"paths": []any{"**", "!docs/**", "docs/keep.md"}}), "refs/heads/main", diffFiles("docs/drop.md"), false, "no changed file"},
		{"paths with branches hit", pushPlan([]string{"push"}, map[string]any{"branches": []any{"main"}, "paths": []any{"src/**"}}), "refs/heads/main", diffFiles("src/a.go"), true, ""},
		{"paths with branches ref miss", pushPlan([]string{"push"}, map[string]any{"branches": []any{"main"}, "paths": []any{"src/**"}}), "refs/heads/dev", diffFiles("src/a.go"), false, "not in branches filter"},
		{"paths diff error skips", pushPlan([]string{"push"}, map[string]any{"paths": []any{"src/**"}}), "refs/heads/main", diffBoom, false, "push diff unavailable"},
		{"paths nil diff skips", pushPlan([]string{"push"}, map[string]any{"paths": []any{"src/**"}}), "refs/heads/main", nil, false, "no diff resolver"},
		{"paths not evaluated for tags", pushPlan([]string{"push"}, map[string]any{"tags": []any{"v*"}, "paths": []any{"src/**"}}), "refs/tags/v1", nil, true, ""},
		{"paths odd ref skips", pushPlan([]string{"push"}, map[string]any{"paths": []any{"src/**"}}), "refs/pull/1/head", nil, false, "no diff resolver"},
	}
	for _, tc := range cases {
		got, why := pushMatchesPlan(context.Background(), tc.plan, tc.ref, tc.diff)
		if got != tc.want {
			t.Errorf("%s: got %v want %v (why %q)", tc.name, got, tc.want, why)
			continue
		}
		if tc.whyRe != "" && !strings.Contains(why, tc.whyRe) {
			t.Errorf("%s: why %q missing %q", tc.name, why, tc.whyRe)
		}
	}
}

func TestPathGlobMatch(t *testing.T) {
	cases := []struct {
		pattern string
		name    string
		want    bool
	}{
		{"*", "README.md", true},
		{"*", "docs/a.md", false},
		{"**", "docs/a.md", true},
		{"docs/*", "docs/a.md", true},
		{"docs/*", "docs/a/b.md", false},
		{"docs/**", "docs/a/b.md", true},
		{"docs/**", "docs", false},
		{"**.go", "main.go", true},
		{"**.go", "a/b/c.go", true},
		{"**.go", "a/b/c.go.txt", false},
		{"**/docs/**", "docs/x.md", true},
		{"**/docs/**", "a/b/docs/x.md", true},
		{"go.mod", "go.mod", true},
		{"go.mod", "x/go.mod", false},
		{"*.jsx?", "page.js", true},
		{"*.jsx?", "page.jsx", true},
		{"*.jsx?", "page.jsxX", false},
		{"src/**", "src/a.go", true},
		{"src/**", "src", false},
	}
	for _, tc := range cases {
		if got := pathGlobMatch(tc.pattern, tc.name); got != tc.want {
			t.Errorf("pathGlobMatch(%q, %q) = %v, want %v", tc.pattern, tc.name, got, tc.want)
		}
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

func wrPlan(trigger map[string]any) *workflows.Plan {
	return &workflows.Plan{
		Events:   []string{"workflow_run"},
		Triggers: map[string]any{"workflow_run": trigger},
	}
}

func TestWorkflowRunMatchesPlan(t *testing.T) {
	cases := []struct {
		name       string
		plan       *workflows.Plan
		action     string
		wfName     string
		wfPath     string
		headBranch string
		want       bool
		whyRe      string
	}{
		{"name hit", wrPlan(map[string]any{"workflows": []any{"Build"}, "types": []any{"completed"}}), "completed", "Build", ".github/workflows/build.yml", "main", true, ""},
		{"name glob", wrPlan(map[string]any{"workflows": []any{"Build*"}, "types": []any{"completed"}}), "completed", "Build C++", "x.yml", "main", true, ""},
		{"path hit", wrPlan(map[string]any{"workflows": []any{".github/workflows/ci.yml"}}), "completed", "CI", ".github/workflows/ci.yml", "main", true, ""},
		{"name miss", wrPlan(map[string]any{"workflows": []any{"Build"}}), "completed", "Tests", "t.yml", "main", false, "not in workflows filter"},
		{"types hit", wrPlan(map[string]any{"workflows": []any{"Build"}, "types": []any{"completed"}}), "completed", "Build", "", "main", true, ""},
		{"types miss", wrPlan(map[string]any{"workflows": []any{"Build"}, "types": []any{"completed"}}), "requested", "Build", "", "main", false, "not in types filter"},
		{"no types matches any", wrPlan(map[string]any{"workflows": []any{"Build"}}), "in_progress", "Build", "", "main", true, ""},
		{"no workflows filter", wrPlan(map[string]any{"types": []any{"completed"}}), "completed", "Build", "", "main", false, "no workflows filter"},
		{"branches hit", wrPlan(map[string]any{"workflows": []any{"Build"}, "branches": []any{"release/**"}}), "completed", "Build", "", "release/1.0", true, ""},
		{"branches miss", wrPlan(map[string]any{"workflows": []any{"Build"}, "branches": []any{"release/**"}}), "completed", "Build", "", "main", false, "not in branches filter"},
		{"branches-ignore hit", wrPlan(map[string]any{"workflows": []any{"Build"}, "branches-ignore": []any{"canary"}}), "completed", "Build", "", "canary", false, "is ignored"},
		{"not subscribed", &workflows.Plan{Events: []string{"push"}, Triggers: map[string]any{}}, "completed", "Build", "", "main", false, ""},
		{"unsupported key", wrPlan(map[string]any{"workflows": []any{"Build"}, "paths": []any{"x"}}), "completed", "Build", "", "main", false, "unsupported workflow_run filter paths"},
	}
	for _, tc := range cases {
		got, why := workflowRunMatchesPlan(tc.plan, tc.action, tc.wfName, tc.wfPath, tc.headBranch)
		if got != tc.want {
			t.Errorf("%s: got %v want %v (why %q)", tc.name, got, tc.want, why)
			continue
		}
		if tc.whyRe != "" && !strings.Contains(why, tc.whyRe) {
			t.Errorf("%s: why %q missing %q", tc.name, why, tc.whyRe)
		}
	}
}

func TestParseWorkflowRunTrigger(t *testing.T) {
	body := []byte(`{
		"action": "completed",
		"workflow_run": {
			"id": 42, "name": "CI", "path": ".github/workflows/ci.yml",
			"event": "push", "status": "completed", "conclusion": "success",
			"head_branch": "main", "head_sha": "abc123",
			"run_number": 7, "run_attempt": 1,
			"html_url": "https://github.com/o/r/actions/runs/42"
		},
		"sender": {"login": "octo"},
		"repository": {"full_name": "o/r", "clone_url": "https://github.com/o/r.git", "default_branch": "main"}
	}`)
	tr, status, _ := parseWorkflowRunTrigger(body)
	if tr == nil {
		t.Fatalf("workflow_run rejected: %s", status)
	}
	if tr.event != "workflow_run" || tr.trust != "trusted" || tr.repoName != "o/r" {
		t.Fatalf("trigger = %#v", tr)
	}
	if tr.defaultBranch != "main" || tr.baseSHA != "abc123" {
		t.Fatalf("default-branch resolution fields = %q %q", tr.defaultBranch, tr.baseSHA)
	}
	if tr.sha != "" || tr.ref != "" {
		t.Fatalf("sha/ref must defer to resolvePRTargetSHA, got %q %q", tr.sha, tr.ref)
	}
	if tr.extra["wr_conclusion"] != "success" || tr.extra["wr_name"] != "CI" {
		t.Fatalf("extras = %#v", tr.extra)
	}
}

func TestParseWorkflowRunTriggerNonDefaultHead(t *testing.T) {
	body := []byte(`{
		"action": "completed",
		"workflow_run": {"name": "CI", "head_branch": "feature-x", "head_sha": "def456"},
		"repository": {"full_name": "o/r", "clone_url": "u", "default_branch": "main"}
	}`)
	tr, _, _ := parseWorkflowRunTrigger(body)
	if tr == nil {
		t.Fatal("workflow_run rejected")
	}
	// head_sha is not the default head here — no fallback, API must resolve.
	if tr.baseSHA != "" {
		t.Fatalf("baseSHA = %q, want empty for non-default head", tr.baseSHA)
	}
}

func TestParseWorkflowRunTriggerMissingFields(t *testing.T) {
	body := []byte(`{"action":"completed","workflow_run":{"conclusion":"success"},"repository":{"full_name":"o/r"}}`)
	tr, status, _ := parseWorkflowRunTrigger(body)
	if tr != nil || status != "ignored" {
		t.Fatalf("empty workflow_run should be ignored, got %v %q", tr, status)
	}
}

func TestParsePushTrigger(t *testing.T) {
	body := []byte(`{
		"ref": "refs/heads/main", "before": "aaa111", "after": "bbb222",
		"head_commit": {"message": "ship it"},
		"pusher": {"name": "tdv"},
		"repository": {"full_name": "o/r", "clone_url": "https://github.com/o/r.git"}
	}`)
	tr, status, _ := parsePushTrigger(body)
	if tr == nil {
		t.Fatalf("push rejected: %s", status)
	}
	if tr.sha != "bbb222" || tr.ref != "refs/heads/main" || tr.trust != "trusted" {
		t.Fatalf("trigger = %#v", tr)
	}
	if tr.pushBefore != "aaa111" || tr.pushAfter != "bbb222" {
		t.Fatalf("diff range = %q %q", tr.pushBefore, tr.pushAfter)
	}
	if tr.pushDiff != nil {
		t.Fatal("pushDiff is installed by actOnGithubEvent, not the parser")
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
