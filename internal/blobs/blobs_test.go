package blobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFilesystemList(t *testing.T) {
	f, err := NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"gh-incoming/1/0", "gh-incoming/1/64", "gh-incoming/2/0", "cache/x/y"} {
		if _, _, err := f.Put(k, strings.NewReader("data")); err != nil {
			t.Fatal(err)
		}
	}
	// A mid-Put temp file must not surface in listings.
	if err := os.WriteFile(filepath.Join(f.root, "gh-incoming/1/.put-deadbeef"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := f.List("gh-incoming/")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"gh-incoming/1/0", "gh-incoming/1/64", "gh-incoming/2/0"}
	if len(keys) != len(want) {
		t.Fatalf("got %v", keys)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("got %v", keys)
		}
	}
	if keys, err := f.List("nonexistent/"); err != nil || len(keys) != 0 {
		t.Fatalf("missing prefix: %v %v", keys, err)
	}
}
