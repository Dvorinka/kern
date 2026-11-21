package orchestrator

import (
	"testing"

	"github.com/dvorinka/kern/internal/store"
)

// Audit coverage for the #371 gating changes: worst-case needs aggregation,
// needs.* shadowing inside called workflows, and the enriched trigger context
// used by concurrency-group interpolation.

func TestNeedsMapAggregateOrder(t *testing.T) {
	cases := []struct {
		name string
		jobs []store.Job
		want string
	}{
		{"all success", []store.Job{doneJob("success", false, ""), doneJob("success", false, "")}, "success"},
		{"skipped only", []store.Job{doneJob("skipped", false, ""), doneJob("skipped", false, "")}, "skipped"},
		{"skipped plus success", []store.Job{doneJob("skipped", false, ""), doneJob("success", false, "")}, "success"},
		{"cancelled plus success", []store.Job{doneJob("cancelled", false, ""), doneJob("success", false, "")}, "cancelled"},
		{"cancelled plus failure", []store.Job{doneJob("cancelled", false, ""), doneJob("failure", false, "")}, "failure"},
		{"skipped plus cancelled", []store.Job{doneJob("skipped", false, ""), doneJob("cancelled", false, "")}, "cancelled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NeedsMap(map[string][]store.Job{"j": tc.jobs})["j"].(map[string]any)["result"]
			if got != tc.want {
				t.Fatalf("result = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNeedsWithAliasesCalleeShadows(t *testing.T) {
	caller := map[string]any{"result": "success"}
	callee := map[string]any{"result": "failure"}
	needs := map[string]any{
		// The caller's own job named "build"...
		"build": caller,
		// ...versus the callee's expanded job "a_build".
		"a_build": callee,
	}
	out := needsWithAliases("a/b", needs)
	if got := out["build"].(map[string]any)["result"]; got != "failure" {
		t.Fatalf("needs.build = %v, want callee's failure to shadow the caller id", got)
	}
	if got := out["a_build"].(map[string]any)["result"]; got != "failure" {
		t.Fatalf("needs.a_build = %v, want failure", got)
	}
}

func TestNeedsWithAliasesNestedPrefixes(t *testing.T) {
	v := map[string]any{"result": "success"}
	needs := map[string]any{"a_b_test": v}
	out := needsWithAliases("a/b/c", needs)
	if out["b_test"] == nil || out["test"] == nil {
		t.Fatalf("expected progressive aliases b_test and test, got %#v", out)
	}
}

func TestTriggerContextFields(t *testing.T) {
	p := &store.CreateRunParams{
		Event:         "push",
		Ref:           "refs/tags/v2.0.0",
		SHA:           "deadbeef",
		Actor:         "octo",
		CommitMessage: "tag it",
		Inputs:        map[string]any{"env": "prod"},
	}
	ctx := triggerContext(p, "Release", map[string]any{"TEAM": "core"})
	gh := ctx["github"].(map[string]any)
	if gh["workflow"] != "Release" || gh["event_name"] != "push" {
		t.Fatalf("workflow/event_name = %#v", gh)
	}
	if gh["ref_name"] != "v2.0.0" || gh["ref"] != "refs/tags/v2.0.0" {
		t.Fatalf("ref fields = %#v", gh)
	}
	if gh["sha"] != "deadbeef" || gh["actor"] != "octo" {
		t.Fatalf("sha/actor = %#v", gh)
	}
	if ctx["vars"].(map[string]any)["TEAM"] != "core" {
		t.Fatalf("vars = %#v", ctx["vars"])
	}
	if ctx["inputs"].(map[string]any)["env"] != "prod" {
		t.Fatalf("inputs = %#v", ctx["inputs"])
	}
	if v := triggerContext(p, "ci", nil)["vars"]; v == nil {
		t.Fatal("nil vars must normalize to an empty map for ${{ vars.X }} lookups")
	}
}
