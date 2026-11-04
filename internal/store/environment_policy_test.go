package store

import "testing"

func TestBranchAllowed(t *testing.T) {
	patterns := []string{"main", "release/*"}
	ok := []string{"refs/heads/main", "main", "refs/heads/release/1.2", "release/1.2"}
	no := []string{"refs/heads/feature", "refs/heads/release/1.2/hotfix", "refs/tags/v1"}
	for _, ref := range ok {
		if !BranchAllowed("selected", patterns, ref) {
			t.Errorf("selected %q should match", ref)
		}
	}
	for _, ref := range no {
		if BranchAllowed("selected", patterns, ref) {
			t.Errorf("selected %q should not match", ref)
		}
	}
	if !BranchAllowed("all", nil, "refs/heads/feature") {
		t.Error("all should allow every ref")
	}
	if BranchAllowed("selected", nil, "refs/heads/main") {
		t.Error("selected with no patterns should deny")
	}
}
