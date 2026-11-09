package runner

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestExtractRunner(t *testing.T) {
	dir := t.TempDir()
	arc := filepath.Join(dir, "r.tar.gz")
	f, err := os.Create(arc)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	want := "kern-runner"
	if runtime.GOOS == "windows" {
		want += ".exe"
	}
	body := []byte("fake-binary")
	if err := tw.WriteHeader(&tar.Header{Name: "./" + want, Size: int64(len(body)), Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	f.Close()
	dest := filepath.Join(dir, "kern-runner.new")
	if err := extractRunner(arc, dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != "fake-binary" {
		t.Fatalf("extract: %v %q", err, got)
	}
	if info, _ := os.Stat(dest); info.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v", info.Mode())
	}
}
