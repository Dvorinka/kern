package main

import "testing"

func TestRiskyEnvKeys(t *testing.T) {
	got := riskyEnvKeys(map[string]string{
		"PATH": "/x", "JAVA_HOME": "/jdk", "DOCKER_HOST": "tcp://bad",
		"GOPROXY": "https://proxy", "https_proxy": "http://p",
	})
	want := []string{"DOCKER_HOST", "PATH", "https_proxy"}
	if len(got) != len(want) {
		t.Fatalf("riskyEnvKeys = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("riskyEnvKeys = %v, want %v", got, want)
		}
	}
	if len(riskyEnvKeys(nil)) != 0 {
		t.Fatal("nil env should yield no keys")
	}
}

func TestFleetNewestVersion(t *testing.T) {
	rows := []runnerRow{
		{Version: "v0.16.0"},
		{Version: "v0.11.0"},
		{Version: "dev"},                    // non-comparable: never sets the bar
		{Version: ""},                       // unreported
		{Version: "v9.9.9", Draining: true}, // draining excluded
	}
	if got := fleetNewestVersion(rows); got != "v0.16.0" {
		t.Fatalf("fleetNewestVersion = %q, want v0.16.0", got)
	}
	if got := fleetNewestVersion([]runnerRow{{Version: "dev"}, {Version: ""}}); got != "" {
		t.Fatalf("fleetNewestVersion with no comparable = %q, want empty", got)
	}
}
