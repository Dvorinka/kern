package orchestrator

import (
	"encoding/json"
	"testing"

	"github.com/dvorinka/kern/internal/store"
)

func doneJob(conclusion string, coe bool, outputs string) store.Job {
	j := store.Job{Status: "completed", Conclusion: conclusion, ContinueOnError: coe}
	if outputs != "" {
		j.Outputs = json.RawMessage(outputs)
	}
	return j
}

func TestDependencyStatus(t *testing.T) {
	build := func(status, conclusion string, coe bool) store.Job {
		return store.Job{Status: status, Conclusion: conclusion, ContinueOnError: coe}
	}
	cases := []struct {
		name     string
		needs    []string
		jobs     map[string][]store.Job
		status   string
		terminal bool
	}{
		{"no needs", nil, nil, "success", true},
		{"missing need", []string{"build"}, map[string][]store.Job{}, "failure", true},
		{"still running", []string{"build"}, map[string][]store.Job{
			"build": {build("in_progress", "", false)},
		}, "", false},
		{"one of a matrix still waiting", []string{"build"}, map[string][]store.Job{
			"build": {build("completed", "success", false), build("waiting", "", false)},
		}, "", false},
		{"failure", []string{"build"}, map[string][]store.Job{
			"build": {build("completed", "failure", false)},
		}, "failure", true},
		{"continue-on-error failure stays success", []string{"build"}, map[string][]store.Job{
			"build": {build("completed", "failure", true)},
		}, "success", true},
		{"cancelled need is a failure for dependents", []string{"build"}, map[string][]store.Job{
			"build": {build("cancelled", "cancelled", false)},
		}, "failure", true},
		{"skipped", []string{"build"}, map[string][]store.Job{
			"build": {build("skipped", "skipped", false)},
		}, "skipped", true},
		{"failure beats a skipped sibling", []string{"build"}, map[string][]store.Job{
			"build": {build("skipped", "skipped", false), build("completed", "failure", false)},
		}, "failure", true},
		{"skipped does not erase an earlier failure", []string{"build"}, map[string][]store.Job{
			"build": {build("completed", "failure", false), build("skipped", "skipped", false)},
		}, "failure", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, terminal := dependencyStatus(tc.needs, tc.jobs)
			if status != tc.status || terminal != tc.terminal {
				t.Fatalf("status=%q terminal=%v, want %q/%v", status, terminal, tc.status, tc.terminal)
			}
		})
	}
}

func TestNeedsMap(t *testing.T) {
	byKey := map[string][]store.Job{
		"build": {
			doneJob("success", false, `{"bin":"a"}`),
			doneJob("failure", true, `{"bin":"b"}`),
		},
		"lint": {doneJob("failure", false, "")},
	}
	needs := NeedsMap(byKey)
	build := needs["build"].(map[string]any)
	if build["result"] != "success" {
		t.Fatalf("continue-on-error result = %v, want success", build["result"])
	}
	outs := build["outputs"].(map[string]any)
	if outs["bin"] != "b" {
		t.Fatalf("outputs = %v, want the later member's bin", outs)
	}
	lint := needs["lint"].(map[string]any)
	if lint["result"] != "failure" {
		t.Fatalf("lint result = %v", lint["result"])
	}
}

func TestNeedsMapLastConclusionWins(t *testing.T) {
	byKey := map[string][]store.Job{
		"build": {doneJob("failure", false, ""), doneJob("success", false, "")},
	}
	build := NeedsMap(byKey)["build"].(map[string]any)
	if build["result"] != "success" {
		t.Fatalf("result = %v, want the last non-empty conclusion", build["result"])
	}
}

func TestJobStatusAndConclusionState(t *testing.T) {
	statuses := map[string]string{
		"in_progress":      "running",
		"queued":           "queued",
		"waiting":          "waiting",
		"waiting_approval": "waiting",
		"skipped":          "skipped",
		"cancelled":        "cancelled",
	}
	for in, want := range statuses {
		if got := jobStatus(store.Job{Status: in}); got != want {
			t.Errorf("status %s → %s, want %s", in, got, want)
		}
	}
	if got := jobStatus(store.Job{Status: "completed", Conclusion: "failure"}); got != "failure" {
		t.Errorf("completed failure → %s", got)
	}
	if got := jobStatus(store.Job{Status: "completed"}); got != "success" {
		t.Errorf("completed without conclusion → %s, want success", got)
	}
	states := map[string]string{"success": "success", "skipped": "success", "failure": "failure", "cancelled": "error", "": "error"}
	for in, want := range states {
		if got := conclusionState(in); got != want {
			t.Errorf("conclusion %q → %s, want %s", in, got, want)
		}
	}
}

func TestEnvAndGitHubRef(t *testing.T) {
	run := store.Run{Event: "push", Ref: "refs/heads/main", SHA: "abc", Actor: "octo"}
	job := store.Job{Key: "build", Env: json.RawMessage(`{"REF":"${{ github.ref_name }}"}`)}
	project := store.Project{Slug: "kern", RepoFullName: "Dvorinka/kern", DefaultBranch: "master"}
	ctx := JobContext(job, run, project, nil)
	gh := ctx["github"].(map[string]any)
	if gh["ref_name"] != "main" || gh["repository"] != "Dvorinka/kern" || gh["repository_owner"] != "Dvorinka" {
		t.Fatalf("github = %#v", gh)
	}
	env, err := Env(ctx, map[string]string{"REF": "${{ github.ref_name }}", "PLAIN": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if env["REF"] != "main" || env["PLAIN"] != "x" {
		t.Fatalf("env = %v", env)
	}
	if _, err := Env(ctx, map[string]string{"BAD": "${{ github.ref"}); err == nil {
		t.Fatal("unterminated expression should fail env expansion")
	}

	empty := store.Run{Event: "workflow_dispatch"}
	gh = githubContext(empty, project, job)
	if gh["ref"] != "refs/heads/master" || gh["ref_name"] != "master" {
		t.Fatalf("empty ref = %#v", gh)
	}
}
