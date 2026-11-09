package main

import "testing"

func TestVersionDrift(t *testing.T) {
	v := func(s string) runnerRow { return runnerRow{Version: s} }

	if got := versionDrift(nil); got != "" {
		t.Fatalf("empty fleet: %q", got)
	}
	if got := versionDrift([]runnerRow{v("v0.7.3"), v("v0.7.3")}); got != "" {
		t.Fatalf("uniform fleet: %q", got)
	}
	got := versionDrift([]runnerRow{v("v0.7.3"), v("v0.7.3"), v("v0.7.1"), v("")})
	want := "versions: unknown×1 v0.7.1×1 v0.7.3×2 — fleet is split, check 'kern runners self-update'"
	if got != want {
		t.Fatalf("drift = %q, want %q", got, want)
	}
}
