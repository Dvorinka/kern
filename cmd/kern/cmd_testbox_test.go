package main

import (
	"testing"

	"github.com/dvorinka/kern/internal/workflows"
)

func TestTestboxDAG(t *testing.T) {
	jobs := []workflows.JobPlan{
		{Key: "build"},
		{Key: "lint"},
		{Key: "test", Needs: []string{"build", "lint"}},
		{Key: "deploy", Needs: []string{"test"}},
		{Key: "docs"}, // unrelated — must not appear
	}
	order, err := testboxDAG(jobs, jobs[3])
	if err != nil {
		t.Fatal(err)
	}
	got := dagNames(order)
	if got != "build → lint → test → deploy" {
		t.Fatalf("order %q", got)
	}
	// Missing dep is an error, not a silent skip.
	broken := []workflows.JobPlan{{Key: "a", Needs: []string{"ghost"}}}
	if _, err := testboxDAG(broken, broken[0]); err == nil {
		t.Fatal("missing needs dep accepted")
	}
	// Cycle guard.
	cycle := []workflows.JobPlan{
		{Key: "a", Needs: []string{"b"}},
		{Key: "b", Needs: []string{"a"}},
	}
	if _, err := testboxDAG(cycle, cycle[0]); err == nil {
		t.Fatal("cycle accepted")
	}
}
