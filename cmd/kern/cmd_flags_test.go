package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// `--` must stop global extraction so remote/subcommand flags survive intact.
func TestExtractGlobalsDoubleDash(t *testing.T) {
	c := &cli{}
	rest := extractGlobals([]string{"ssh", "abc", "ls", "--", "--server", "x", "--json"}, c)
	want := []string{"ssh", "abc", "ls", "--", "--server", "x", "--json"}
	if !reflect.DeepEqual(rest, want) {
		t.Fatalf("rest = %v, want %v", rest, want)
	}
	if c.server != "" || c.json {
		t.Fatalf("globals leaked past --: server=%q json=%v", c.server, c.json)
	}
}

// Globals before -- are still consumed.
func TestExtractGlobalsBeforeDoubleDash(t *testing.T) {
	c := &cli{}
	rest := extractGlobals([]string{"--json", "runs", "wait", "--", "--project", "p"}, c)
	want := []string{"runs", "wait", "--", "--project", "p"}
	if !reflect.DeepEqual(rest, want) {
		t.Fatalf("rest = %v, want %v", rest, want)
	}
	if !c.json || c.project != "" {
		t.Fatalf("json=%v project=%q", c.json, c.project)
	}
}

// parseFlagsEx treats everything after -- as positional.
func TestParseFlagsDoubleDash(t *testing.T) {
	opts, pos := parseFlagsEx([]string{"run-1", "--", "--job", "build"}, nil, "job")
	if opts["job"] != "" {
		t.Fatalf("flag parsed past --: %v", opts)
	}
	want := []string{"run-1", "--job", "build"}
	if !reflect.DeepEqual(pos, want) {
		t.Fatalf("pos = %v, want %v", pos, want)
	}
}

func TestRepoRootFor(t *testing.T) {
	dir := t.TempDir()
	wfDir := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(wfDir, "ci.yml")
	if err := os.WriteFile(file, []byte("name: ci\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := repoRootFor(file); got != dir {
		t.Fatalf("repoRootFor(file) = %q, want %q", got, dir)
	}
	if got := repoRootFor(dir); got != dir {
		t.Fatalf("repoRootFor(dir) = %q, want %q", got, dir)
	}
	// A file outside any .github/workflows tree roots at its own directory.
	loose := filepath.Join(dir, "loose.yml")
	if err := os.WriteFile(loose, []byte("name: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := repoRootFor(loose); got != dir {
		t.Fatalf("repoRootFor(loose) = %q, want %q", got, dir)
	}
}
