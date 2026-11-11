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
