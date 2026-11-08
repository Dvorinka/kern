package runner

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestSweepWorkspaces(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-48 * time.Hour)
	mk := func(name string, mtime time.Time) {
		d := filepath.Join(root, name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(d, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	mk("stale-job-dir", old)        // removed
	mk("fresh-job-dir", time.Now()) // kept: too new
	mk("_cache", old)               // kept: managed cache
	mk("_actions", old)             // kept: managed cache
	if err := os.WriteFile(filepath.Join(root, "image-usage.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := &Agent{cfg: Config{WorkRoot: root}, log: zap.NewNop()}
	a.sweepWorkspaces()

	for _, gone := range []string{"stale-job-dir"} {
		if _, err := os.Stat(filepath.Join(root, gone)); !os.IsNotExist(err) {
			t.Errorf("%s should be removed", gone)
		}
	}
	for _, kept := range []string{"fresh-job-dir", "_cache", "_actions", "image-usage.json"} {
		if _, err := os.Stat(filepath.Join(root, kept)); err != nil {
			t.Errorf("%s should be kept: %v", kept, err)
		}
	}
}

func TestSweepBoundedCache(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(root, "_cache")
	old := time.Now().Add(-48 * time.Hour)
	write := func(name string, size int, mtime time.Time) string {
		p := filepath.Join(cache, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
		return p
	}
	oldBig := write("old.bin", 600, old)
	newBig := write("new.bin", 600, time.Now())

	// Cap of 700 bytes against 1200 total: the oldest file goes, the fresh
	// one stays even though it alone nearly fills the cap.
	a := &Agent{cfg: Config{WorkRoot: root, CacheMaxBytes: 700}, log: zap.NewNop()}
	a.sweepBoundedCache()
	if _, err := os.Stat(oldBig); !os.IsNotExist(err) {
		t.Error("oldest cache file should be evicted")
	}
	if _, err := os.Stat(newBig); err != nil {
		t.Error("fresh cache file should be kept")
	}

	// A job in flight freezes eviction entirely.
	write("old2.bin", 600, old)
	a.active.Store(1)
	a.sweepBoundedCache()
	a.active.Store(0)
	if _, err := os.Stat(filepath.Join(cache, "old2.bin")); err != nil {
		t.Error("cache bound must not run while a job is active")
	}

	// Zero cap disables the bound.
	write("old3.bin", 600, old)
	a.cfg.CacheMaxBytes = 0
	a.sweepBoundedCache()
	if _, err := os.Stat(filepath.Join(cache, "old2.bin")); err != nil {
		t.Error("cache bound disabled must evict nothing")
	}
}

func TestDiskUnderPressure(t *testing.T) {
	cases := []struct {
		free, total int64
		pct         float64
		want        bool
	}{
		{5 << 30, 100 << 30, 10, true},   // 5% free < 10%
		{20 << 30, 100 << 30, 10, false}, // 20% free
		{5 << 30, 100 << 30, 0, false},   // disabled
		{0, 0, 10, false},                // statfs failure
	}
	for _, c := range cases {
		if got := diskUnderPressure(c.free, c.total, c.pct); got != c.want {
			t.Errorf("diskUnderPressure(%d,%d,%v) = %v, want %v", c.free, c.total, c.pct, got, c.want)
		}
	}
}
