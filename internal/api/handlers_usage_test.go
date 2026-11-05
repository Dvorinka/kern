package api

import "testing"

func TestNewerThan(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"0.2.0", "v0.3.0", true},
		{"v0.2.0", "v0.2.0", false},
		{"0.3.0", "v0.2.0", false},
		{"v0.2.0", "v0.2.1", true},
		{"v0.2.0", "v1.0.0", true},
		{"dev", "v0.2.0", false},
		{"v0.2.0", "latest", false},
		{"v0.2.0-rc1", "v0.2.0", false}, // prerelease suffix parses to same triple
		{"0.2", "v0.2.0", false},
		{"", "v0.2.0", false},
	}
	for _, tc := range cases {
		if got := newerThan(tc.current, tc.latest); got != tc.want {
			t.Errorf("newerThan(%q, %q) = %v, want %v", tc.current, tc.latest, got, tc.want)
		}
	}
}
