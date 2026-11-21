package api

import (
	"net"
	"testing"
)

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

func TestPublicNotifyIP(t *testing.T) {
	for _, tc := range []struct {
		ip   string
		want bool
	}{
		{"8.8.8.8", true},
		{"1.1.1.1", true},
		{"2606:4700:4700::1111", true},
		{"127.0.0.1", false},
		{"::1", false},
		{"10.0.0.1", false},
		{"172.16.0.1", false},
		{"192.168.1.1", false},
		{"169.254.169.254", false}, // cloud metadata
		{"fe80::1", false},
		{"0.0.0.0", false},
		{"224.0.0.1", false},
		{"::ffff:127.0.0.1", false}, // v4-mapped loopback
	} {
		if got := publicNotifyIP(net.ParseIP(tc.ip)); got != tc.want {
			t.Errorf("publicNotifyIP(%s) = %v want %v", tc.ip, got, tc.want)
		}
	}
}

func TestNotifyURLHostSafe(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{"https://hooks.example.com/x", true},
		{"http://10.0.0.1/hook", false},
		{"http://169.254.169.254/latest/meta-data", false},
		{"http://[::1]/x", false},
		{"http://127.0.0.1:8080/", false},
		{"not a url", false},
		{"", false},
	} {
		if got := notifyURLHostSafe(tc.raw); got != tc.want {
			t.Errorf("notifyURLHostSafe(%q) = %v want %v", tc.raw, got, tc.want)
		}
	}
}
