package api

import "testing"

func TestNotifyConclusionAllowed(t *testing.T) {
	cases := []struct {
		filter, conclusion string
		want               bool
	}{
		{"", "success", true},
		{"", "failure", true},
		{"failure", "failure", true},
		{"failure,cancelled", "success", false},
		{" Failure , cancelled ", "cancelled", true},
		{"SUCCESS", "success", true},
	}
	for _, tc := range cases {
		if got := notifyConclusionAllowed(tc.filter, tc.conclusion); got != tc.want {
			t.Errorf("notifyConclusionAllowed(%q,%q)=%v want %v", tc.filter, tc.conclusion, got, tc.want)
		}
	}
}

func TestValidNotifyEvents(t *testing.T) {
	for _, ok := range []string{"", "failure", "success,failure,cancelled,skipped"} {
		if !validNotifyEvents(ok) {
			t.Errorf("validNotifyEvents(%q) = false", ok)
		}
	}
	for _, bad := range []string{"bogus", "success,wat"} {
		if validNotifyEvents(bad) {
			t.Errorf("validNotifyEvents(%q) = true", bad)
		}
	}
}
