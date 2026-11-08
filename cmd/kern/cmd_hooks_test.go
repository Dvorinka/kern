package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitHookPath(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	// gitHookPath resolves against the process cwd, so chdir into the repo.
	old, _ := os.Getwd()
	defer os.Chdir(old)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	p, err := gitHookPath("pre-push")
	if err != nil {
		t.Fatalf("gitHookPath: %v", err)
	}
	want := filepath.Join(dir, ".git", "hooks", "pre-push")
	if p != want {
		t.Fatalf("gitHookPath = %q, want %q", p, want)
	}
}

func TestPrePushHookContent(t *testing.T) {
	if !strings.HasPrefix(prePushHook, "#!/bin/sh\n") {
		t.Fatal("hook must start with a shebang")
	}
	if !strings.Contains(prePushHook, "kern lint") {
		t.Fatal("hook must run kern lint")
	}
	if !strings.Contains(prePushHook, hookMarker) {
		t.Fatal("hook must carry the install marker")
	}
}
