package orchestrator

import (
	"errors"
	"testing"

	"github.com/dvorinka/kern/internal/store"
)

func TestSplitWidth(t *testing.T) {
	cases := []struct {
		requested, available, siblings, want int
	}{
		{4, 0, 1, 4},
		{1, 99, 1, 1},
		{40, 0, 1, 32},
		{0, 0, 1, 1},
		{0, 3, 1, 3},
		{0, 100, 1, 16},
		{20, 0, 20, 12},
		{4, 0, 100, 2},
		{4, 0, 300, 1},
	}
	for _, c := range cases {
		got := splitWidth(c.requested, c.available, c.siblings)
		if got != c.want {
			t.Errorf("splitWidth(%d, %d, %d) = %d, want %d", c.requested, c.available, c.siblings, got, c.want)
		}
	}
}

func TestExpandSplits(t *testing.T) {
	seeds := []store.JobSeed{
		{Key: "build", JobKey: "build", Name: "Build", Env: map[string]string{"A": "1"}, SplitN: 2,
			Steps: []store.StepSeed{{Key: "run", Name: "run"}}},
		{Key: "lint", JobKey: "lint", Name: "Lint"},
		{Key: "test (go: 1.23)", JobKey: "test", Name: "Test", Matrix: map[string]any{"go": "1.23"}, SplitAuto: true},
		{Key: "test (go: 1.24)", JobKey: "test", Name: "Test", Matrix: map[string]any{"go": "1.24"}, SplitAuto: true},
	}
	out, err := expandSplits(seeds, func(s store.JobSeed, siblings int) (int, error) {
		if s.SplitAuto {
			if siblings != 2 {
				t.Errorf("siblings = %d, want 2", siblings)
			}
			return splitWidth(0, 4, siblings), nil
		}
		return splitWidth(s.SplitN, 0, siblings), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// build × 2, lint × 1, each test member × 4 (auto width 4, under the 256 cap).
	if len(out) != 2+1+8 {
		t.Fatalf("jobs = %d, want 11", len(out))
	}
	if out[0].Key != "build (shard 1/2)" || out[0].JobKey != "build" || out[0].Env["KERN_SHARD"] != "1" || out[0].Env["KERN_SHARDS"] != "2" {
		t.Fatalf("first shard: %+v", out[0])
	}
	if out[0].Matrix["shard"] != 1 || out[1].Name != "Build (shard 2/2)" {
		t.Fatalf("shard identity: %+v %+v", out[0].Matrix, out[1])
	}
	out[0].Env["A"] = "mutated"
	if out[1].Env["A"] != "1" {
		t.Fatal("shard env maps are aliased")
	}
	if out[2].Key != "lint" || out[2].Env != nil {
		t.Fatalf("unsplittable job changed: %+v", out[2])
	}
	if out[3].Matrix["go"] != "1.23" || out[3].Matrix["shard"] != 1 || out[3].Env["KERN_SHARDS"] != "4" {
		t.Fatalf("matrix shard: %+v env %+v", out[3].Matrix, out[3].Env)
	}
	if len(out[0].Steps) != 1 || out[0].Steps[0].Key != "run" {
		t.Fatalf("steps: %+v", out[0].Steps)
	}

	_, err = expandSplits([]store.JobSeed{{JobKey: "a", SplitN: 2}}, func(store.JobSeed, int) (int, error) {
		return 0, errors.New("fleet down")
	})
	if err == nil {
		t.Fatal("width error was swallowed")
	}
}
