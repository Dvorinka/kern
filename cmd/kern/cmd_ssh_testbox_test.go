package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dvorinka/kern/internal/workflows"
)

func TestParseSSHArgs(t *testing.T) {
	target, job, cmd, err := parseSSHArgs([]string{"abc-123", "-c", "ls -la"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if target != "abc-123" || job != "" || cmd != "ls -la" {
		t.Fatalf("got target=%q job=%q cmd=%q", target, job, cmd)
	}

	target, job, cmd, err = parseSSHArgs([]string{"abc-123", "--job", "build", "-c=uname -a"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if target != "abc-123" || job != "build" || cmd != "uname -a" {
		t.Fatalf("got target=%q job=%q cmd=%q", target, job, cmd)
	}

	target, job, cmd, err = parseSSHArgs([]string{"abc-123", "--", "curl", "--json", "{}"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if target != "abc-123" || cmd != "curl --json {}" {
		t.Fatalf("terminator broken: target=%q cmd=%q", target, cmd)
	}

	if _, _, _, err := parseSSHArgs([]string{"abc-123", "-c"}); err == nil {
		t.Fatal("expected error for missing -c argument")
	}
	if _, _, _, err := parseSSHArgs([]string{"abc-123", "--bogus"}); err == nil {
		t.Fatal("expected error for unknown flag")
	}
}

func TestLoadEnvFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	content := "# comment\n\nPLAIN=hello\nexport EXPORTED=\"quoted val\"\nSINGLE='keep inner \"quotes\"'\n  SPACED = raw\n"
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	if err := loadEnvFile(p, got); err != nil {
		t.Fatalf("loadEnvFile: %v", err)
	}
	want := map[string]string{
		"PLAIN":    "hello",
		"EXPORTED": "quoted val",
		"SINGLE":   `keep inner "quotes"`,
		"SPACED":   " raw",
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %q, want %q", k, got[k], w)
		}
	}
}

func TestTestboxClaimDefaults(t *testing.T) {
	dir := t.TempDir()
	res := workflows.Result{Name: "ci"}
	job := workflows.JobPlan{Key: "test"}

	claim := testboxClaim(res, job, dir, map[string]string{})
	if claim.Run.TrustLevel != "untrusted" {
		t.Fatalf("default TrustLevel = %q, want untrusted", claim.Run.TrustLevel)
	}
	if claim.Run.Event != "push" {
		t.Fatalf("default Event = %q, want push", claim.Run.Event)
	}
	if claim.Run.Ref != "refs/heads/main" {
		t.Fatalf("fallback Ref = %q, want refs/heads/main", claim.Run.Ref)
	}

	priv := testboxClaim(res, job, dir, map[string]string{
		"privileged": "true",
		"event":      "pull_request",
		"ref":        "refs/heads/feat",
	})
	if priv.Run.TrustLevel != "trusted" || priv.Run.Event != "pull_request" || priv.Run.Ref != "refs/heads/feat" {
		t.Fatalf("overrides failed: %+v", priv.Run)
	}
}
