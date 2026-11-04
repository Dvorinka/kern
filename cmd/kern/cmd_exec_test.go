package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The content digest must ignore tar metadata: same files, different mtimes,
// identical digest — that is what makes server-side dedupe hit.
func TestBundleDigestStable(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "alpha")
	writeFile(t, dir, "sub/b.txt", "beta")

	_, _, d1, err := bundleDir(dir)
	if err != nil {
		t.Fatalf("bundle: %v", err)
	}
	past := time.Now().Add(-time.Hour)
	_ = os.Chtimes(filepath.Join(dir, "a.txt"), past, past)
	_, _, d2, err := bundleDir(dir)
	if err != nil {
		t.Fatalf("bundle 2: %v", err)
	}
	if d1 != d2 {
		t.Fatalf("digest changed on mtime-only difference: %s vs %s", d1, d2)
	}

	writeFile(t, dir, "a.txt", "alpha-changed")
	_, _, d3, err := bundleDir(dir)
	if err != nil {
		t.Fatalf("bundle 3: %v", err)
	}
	if d3 == d1 {
		t.Fatal("digest did not change when file content changed")
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
