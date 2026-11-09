package store

import (
	"testing"

	"github.com/google/uuid"
)

func mkRerunJob(jobKey, key string, needs []string, conclusion string) rerunJob {
	return rerunJob{ID: uuid.New(), JobKey: jobKey, Key: key, Needs: needs, Conclusion: conclusion}
}

func hasID(set map[uuid.UUID]bool, j rerunJob) bool { return set[j.ID] }

func TestRerunSeedFailedSelectsNonSuccessAndDependents(t *testing.T) {
	fail := mkRerunJob("build", "build", nil, "failure")
	cancelled := mkRerunJob("lint", "lint", nil, "cancelled")
	success := mkRerunJob("docs", "docs", nil, "success")
	skippedDep := mkRerunJob("test", "test", []string{"build"}, "skipped")
	successDep := mkRerunJob("deploy", "deploy", []string{"test"}, "success")
	unrelated := mkRerunJob("nightly", "nightly", nil, "success")
	jobs := []rerunJob{fail, cancelled, success, skippedDep, successDep, unrelated}

	got := rerunSeed(jobs, nil)
	for _, j := range []rerunJob{fail, cancelled, skippedDep, successDep} {
		if !hasID(got, j) {
			t.Errorf("expected %s (%s) in rerun set", j.Key, j.Conclusion)
		}
	}
	for _, j := range []rerunJob{success, unrelated} {
		if hasID(got, j) {
			t.Errorf("did not expect %s in rerun set", j.Key)
		}
	}
}

func TestRerunSeedJobKeysAndMatrix(t *testing.T) {
	m1 := mkRerunJob("build", "build (linux)", nil, "success")
	m2 := mkRerunJob("build", "build (darwin)", nil, "failure")
	dep := mkRerunJob("publish", "publish", []string{"build"}, "success")
	other := mkRerunJob("lint", "lint", nil, "failure")
	jobs := []rerunJob{m1, m2, dep, other}

	// Selecting by shared job_key pulls every matrix member.
	got := rerunSeed(jobs, []string{"build"})
	if !hasID(got, m1) || !hasID(got, m2) || !hasID(got, dep) {
		t.Error("job_key selection should include all matrix members and dependents")
	}
	if hasID(got, other) {
		t.Error("unrelated failed job must not be pulled by --job")
	}

	// Selecting by unique key pulls just that member (plus dependents).
	got = rerunSeed(jobs, []string{"build (darwin)"})
	if hasID(got, m1) {
		t.Error("unique-key selection must not pull sibling matrix members")
	}
	if !hasID(got, m2) || !hasID(got, dep) {
		t.Error("unique-key selection should include the member and dependents")
	}
}

func TestRerunSeedNoMatch(t *testing.T) {
	ok := mkRerunJob("build", "build", nil, "success")
	if got := rerunSeed([]rerunJob{ok}, []string{"nope"}); len(got) != 0 {
		t.Error("expected empty set for unmatched key")
	}
	if got := rerunSeed([]rerunJob{ok}, nil); len(got) != 0 {
		t.Error("expected empty set when everything succeeded")
	}
}
