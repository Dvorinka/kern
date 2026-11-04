package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/dvorinka/kern/internal/store"
)

func TestPublicViewOmitsPrivateFields(t *testing.T) {
	run := store.Run{
		ID:           uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		WorkflowName: "CI", RunNumber: 3, Event: "push",
		Ref: "refs/heads/master", SHA: "abcdef1234567890",
		CommitMessage: "fix the gate\nTOKEN=supersecret",
		Actor:         "admin@kern.local", Status: "completed", Conclusion: "success",
		Source:      json.RawMessage(`{"token":"supersecret"}`),
		ProjectSlug: "kern",
	}
	jobs := []store.Job{{
		Name: "go", Status: "completed", Conclusion: "success",
		Env:   json.RawMessage(`{"TOKEN":"supersecret"}`),
		Error: "boom TOKEN=supersecret",
		Steps: []store.Step{{
			Number: 1, Name: "test", Status: "completed", Conclusion: "success",
			Spec: json.RawMessage(`{"run":"echo supersecret"}`),
		}},
	}}
	view, pjobs := publicView(run, jobs)
	b, err := json.Marshal(map[string]any{"run": view, "jobs": pjobs})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, bad := range []string{"supersecret", "TOKEN", "echo", "the gate\n"} {
		if strings.Contains(s, bad) {
			t.Fatalf("leaked %q in %s", bad, s)
		}
	}
	if view.Commit != "fix the gate" {
		t.Fatalf("commit %q", view.Commit)
	}
	if view.Actor != "" {
		t.Fatalf("email actor leaked: %q", view.Actor)
	}
	if pjobs[0].Steps[0].Name != "test" || len(pjobs[0].Steps) != 1 {
		t.Fatalf("steps %+v", pjobs[0].Steps)
	}

	login, _ := publicView(store.Run{ID: run.ID, Actor: "octocat"}, nil)
	if login.Actor != "octocat" {
		t.Fatalf("login actor %q", login.Actor)
	}
}

func TestGitHubCheckAndLink(t *testing.T) {
	pending, c := githubCheck("pending", store.Run{})
	if pending != "in_progress" || c != "" {
		t.Fatalf("pending %s %s", pending, c)
	}
	st, c := githubCheck("error", store.Run{Conclusion: "cancelled"})
	if st != "completed" || c != "cancelled" {
		t.Fatalf("cancelled %s %s", st, c)
	}
	st, c = githubCheck("success", store.Run{Conclusion: "skipped"})
	if st != "completed" || c != "skipped" {
		t.Fatalf("skipped %s %s", st, c)
	}
	if runPagePath(false, "abc") != "/runs/abc" || runPagePath(true, "abc") != "/ci/abc" {
		t.Fatal(runPagePath(false, "abc"), runPagePath(true, "abc"))
	}
	desc := statusDescription(store.Run{WorkflowName: "CI"}, "failure", []store.Job{
		{Conclusion: "success"}, {Conclusion: "failure"},
	})
	if desc != "KERN CI: failure (1/2 jobs failed)" {
		t.Fatal(desc)
	}
	sum := checkSummary(store.Run{WorkflowName: "CI", RunNumber: 1, Event: "push", SHA: "abcdef1234"}, []store.Job{
		{Name: "go|test", Conclusion: "success", Steps: []store.Step{{Name: "vet", Conclusion: "success"}}},
	})
	if strings.Contains(sum, "go|test") || !strings.Contains(sum, "go/test") || !strings.Contains(sum, "abcdef1") {
		t.Fatal(sum)
	}
}

func TestOrgSettingWrites(t *testing.T) {
	off := "off"
	bad := "sometimes"
	on := "on"
	writes, msg := orgSettingWrites(&off, nil)
	if msg != "" || writes["self_heal"] != "off" || len(writes) != 1 {
		t.Fatalf("self_heal only: %v %q", writes, msg)
	}
	writes, msg = orgSettingWrites(nil, &on)
	if msg != "" || writes["public_ci"] != "on" {
		t.Fatalf("public only: %v %q", writes, msg)
	}
	if _, msg = orgSettingWrites(nil, nil); msg == "" {
		t.Fatal("empty body must fail")
	}
	if _, msg = orgSettingWrites(&bad, nil); msg == "" {
		t.Fatal("bad self_heal must fail")
	}
	if _, msg = orgSettingWrites(nil, &bad); msg == "" {
		t.Fatal("bad public_ci must fail")
	}
}
