package orchestrator

import (
	"slices"
	"testing"

	"github.com/dvorinka/kern/internal/store"
	"github.com/dvorinka/kern/internal/workflows"
	"go.uber.org/zap"
)

func TestRunsOnCovered(t *testing.T) {
	labels := []string{"self-hosted", "kern", "linux-x64", "ubuntu-latest", "docker", "ci-runner-901"}
	cases := []struct {
		runsOn []string
		want   bool
	}{
		{[]string{"self-hosted"}, true},
		{[]string{"ubuntu-latest"}, true},
		{[]string{"ubuntu-latest", "docker"}, true},
		{[]string{"ci-runner-901"}, true},
		{[]string{"macos"}, false},
		{[]string{"ubuntu-latest", "arm64"}, false},
		// The implicit labels cover jobs that ask for them even when the
		// stored row lacks them.
		{[]string{"kern"}, true},
	}
	for _, c := range cases {
		if got := runsOnCovered(c.runsOn, labels); got != c.want {
			t.Errorf("runsOnCovered(%v) = %v, want %v", c.runsOn, got, c.want)
		}
	}
	if !runsOnCovered([]string{"kern"}, []string{"linux"}) {
		t.Error("implicit kern label must satisfy runs_on")
	}
}

func TestExpandEachRunner(t *testing.T) {
	s, ctx := testStore(t)
	projectID, _, _ := fixture(t, ctx, s)
	org, err := s.FirstOrg(ctx)
	if err != nil {
		t.Fatalf("org: %v", err)
	}
	mk := func(name string, labels []string) {
		t.Helper()
		if _, err := s.RegisterRunner(ctx, store.RegisterRunnerParams{
			OrgID: org.ID, Name: name, Labels: labels, TokenHash: "h-" + name,
		}); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}
	base := []string{"self-hosted", "kern", "linux", "linux-x64", "ubuntu-latest", "docker"}
	mk("runner-a", append(slices.Clone(base), "runner-a"))
	mk("runner-b", append(slices.Clone(base), "runner-b"))
	// Runners whose stored labels omit their own name are still pinned by name
	// because ClaimJob implicitly includes r.name in effective labels — avoiding
	// duplicate job keys when multiple such runners exist.
	mk("old-runner", slices.Clone(base))
	// A macOS runner must not pick up linux jobs.
	mk("mac-mini", []string{"self-hosted", "kern", "macos", "x64", "mac-mini"})

	o := New(s, zap.NewNop(), workflows.Compiler{})
	seed := store.JobSeed{Key: "build", JobKey: "build", Name: "Build",
		RunsOn: []string{"ubuntu-latest"}, Steps: stepSeed()}

	out, err := o.expandEachRunner(ctx, projectID, []store.JobSeed{seed}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("jobs = %d, want 3 (runner-a, runner-b, old-runner)", len(out))
	}
	pinned := map[string]bool{}
	for _, j := range out {
		for _, name := range []string{"runner-a", "runner-b", "old-runner"} {
			if slices.Contains(j.RunsOn, name) {
				pinned[name] = true
				if j.Key != "build @ "+name {
					t.Errorf("pinned key = %q, want %q", j.Key, "build @ "+name)
				}
			}
		}
	}
	if !pinned["runner-a"] || !pinned["runner-b"] || !pinned["old-runner"] {
		t.Fatalf("fan-out wrong: pinned=%v", pinned)
	}

	// RunnerNames limits the fan-out — auto-prime pins just the new runner.
	out, err = o.expandEachRunner(ctx, projectID, []store.JobSeed{seed}, []string{"runner-a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || !slices.Contains(out[0].RunsOn, "runner-a") {
		t.Fatalf("filtered fan-out: %+v", out)
	}

	// A seed no online runner covers survives unpinned instead of vanishing.
	mac := store.JobSeed{Key: "mac", JobKey: "mac", Name: "Mac",
		RunsOn: []string{"ubuntu-latest", "gpu"}, Steps: stepSeed()}
	out, err = o.expandEachRunner(ctx, projectID, []store.JobSeed{mac}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Key != "mac" {
		t.Fatalf("uncovered seed: %+v", out)
	}
}
