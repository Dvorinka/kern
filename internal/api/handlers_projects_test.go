package api

import "testing"

func TestPrNumberFromRef(t *testing.T) {
	for _, tc := range []struct {
		ref  string
		want int
		ok   bool
	}{
		{"refs/pull/422/head", 422, true},
		{"refs/pull/7/merge", 0, false},
		{"refs/heads/main", 0, false},
		{"refs/pull//head", 0, false},
	} {
		n, ok := prNumberFromRef(tc.ref)
		if n != tc.want || ok != tc.ok {
			t.Fatalf("prNumberFromRef(%q) = %d,%v want %d,%v", tc.ref, n, ok, tc.want, tc.ok)
		}
	}
}

func TestNormWorkflowPath(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"kern/ci.yml", "kern/ci.yml"},
		{".github/workflows/x.yml", ".github/workflows/x.yml"},
		{"./kern/ci.yml", "kern/ci.yml"},
		{"/kern/ci.yml", "kern/ci.yml"},
		{" kern\\ci.yml ", "kern/ci.yml"},
		{"", ""},
		{".", ""},
		{"../x.yml", ""},
		{"a/../../b.yml", ""},
		{"a/../b.yml", ""},
	} {
		if got := normWorkflowPath(tc.in); got != tc.want {
			t.Fatalf("normWorkflowPath(%q) = %q want %q", tc.in, got, tc.want)
		}
	}
}
