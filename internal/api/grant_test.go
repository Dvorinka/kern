package api

import (
	"testing"

	"github.com/dvorinka/kern/internal/workflows"
)

func TestCleanGrantList(t *testing.T) {
	got, err := cleanGrantList("project", []string{" Runway ", "runway", "other"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "runway" || got[1] != "other" {
		t.Fatalf("got %v", got)
	}
	if _, err := cleanGrantList("label", []string{"pool a"}, false); err == nil {
		t.Fatal("space in a label was accepted")
	}
	long := make([]string, 33)
	for i := range long {
		long[i] = "x"
	}
	if _, err := cleanGrantList("project", long, true); err == nil {
		t.Fatal("33 projects was accepted")
	}
}

func TestNarrowList(t *testing.T) {
	if err := narrowList("project", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := narrowList("project", []string{"a"}, nil); err == nil {
		t.Fatal("scoped parent minted an unrestricted child")
	}
	if err := narrowList("project", []string{"a"}, []string{"a", "b"}); err == nil {
		t.Fatal("child widened the grant")
	}
	if err := narrowList("label", []string{"pool-a", "linux"}, []string{"pool-a"}); err != nil {
		t.Fatal(err)
	}
}

func TestRunsOnDenial(t *testing.T) {
	if runsOnDenial(nil, nil) != "" {
		t.Fatal("unrestricted token denied a plan")
	}
	ok := []workflows.JobPlan{{Key: "build", RunsOn: []string{"pool-a", "linux"}}}
	if msg := runsOnDenial([]string{"pool-a", "linux", "docker"}, ok); msg != "" {
		t.Fatal(msg)
	}
	if msg := runsOnDenial([]string{"pool-a"}, ok); msg == "" {
		t.Fatal("label outside the pool was allowed")
	}
	if msg := runsOnDenial([]string{"pool-a"}, []workflows.JobPlan{{Key: "build"}}); msg == "" {
		t.Fatal("empty runs-on was allowed")
	}
}

func TestGrantRouteAllowed(t *testing.T) {
	if !grantRouteAllowed("/api/v1/projects/:project/runs") {
		t.Fatal("project dispatch route denied")
	}
	if !grantRouteAllowed("/api/v1/runs/:id/cancel") {
		t.Fatal("cancel denied")
	}
	if grantRouteAllowed("/api/v1/secrets") || grantRouteAllowed("/api/v1/mcp") || grantRouteAllowed("") {
		t.Fatal("org-wide route allowed")
	}
}
