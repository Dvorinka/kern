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
