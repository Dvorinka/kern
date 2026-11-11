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

// Extracted module trees, tool installs and action checkouts are atomic:
// eviction removes the whole entry or leaves it intact — never loose
// files inside one (go stamps module files with old mtimes, so file-level
// eviction shredded gomod first on the fleet).
func TestSweepBoundedCacheAtomicUnits(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(root, "_cache")
	old := time.Now().Add(-48 * time.Hour)
	write := func(name string, size int, mtime time.Time) {
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
	}
	write("gomod/github.com/foo/bar@v1.0.0/a.go", 300, old)
	write("gomod/github.com/foo/bar@v1.0.0/sub/b.go", 300, old)
	write("gomod/github.com/foo/baz@v2.0.0/c.go", 300, time.Now())
	write("_toolcache/go/1.26.10/x64/bin/go", 100, old)
	write("_toolcache/go/1.26.10.complete", 10, old)
	write("gocache/aa/newer", 290, time.Now())

	// Total 1300 against a 600 cap: evicts the cold module tree (600),
	// then the cold tool install (110) — the marker dies with it. The
	// fresh module and flat-section file stay.
	a := &Agent{cfg: Config{WorkRoot: root, CacheMaxBytes: 600}, log: zap.NewNop()}
	a.sweepBoundedCache()

	for _, gone := range []string{
		"gomod/github.com/foo/bar@v1.0.0",
		"_toolcache/go/1.26.10",
		"_toolcache/go/1.26.10.complete",
	} {
		if _, err := os.Stat(filepath.Join(cache, gone)); !os.IsNotExist(err) {
			t.Errorf("%s should be evicted whole", gone)
		}
	}
	for _, kept := range []string{
		"gomod/github.com/foo/baz@v2.0.0/c.go",
		"gocache/aa/newer",
	} {
		if _, err := os.Stat(filepath.Join(cache, kept)); err != nil {
			t.Errorf("%s should be kept: %v", kept, err)
		}
	}
}

func TestCacheEvictionUnit(t *testing.T) {
	cases := []struct {
		rel  string
		want string
		dir  bool
	}{
		{"gomod/github.com/foo/bar@v1.0.0/a.go", "gomod/github.com/foo/bar@v1.0.0", true},
		{"gomod/cache/download/github.com/foo/@v/v1.0.0.zip", "gomod/cache/download/github.com/foo/@v", true},
		{"gomod/cache/lock", "gomod/cache/lock", false},
		{"_toolcache/go/1.26.10/x64/bin/go", "_toolcache/go/1.26.10", true},
		{"_toolcache/go/1.26.10.complete", "_toolcache/go/1.26.10", true},
		{"_actions/actions__checkout__abc123/dist/index.js", "_actions/actions__checkout__abc123", true},
		{"ms-playwright/chromium-1187/chrome-linux/chrome", "ms-playwright/chromium-1187", true},
		{"cargo/registry/src/index.crates.io-6f1b/serde-1.0.0/src/lib.rs", "cargo/registry/src/index.crates.io-6f1b/serde-1.0.0", true},
		{"cargo/registry/cache/index.crates.io-6f1b/serde-1.0.0.crate", "cargo/registry/cache/index.crates.io-6f1b/serde-1.0.0.crate", false},
		{"cargo/registry/index/index.crates.io-6f1b/.cache/se/rd/serde", "cargo/registry/index/index.crates.io-6f1b/.cache/se/rd/serde", false},
		{"cargo/git/db/foo-abc123/objects/pack/p1", "cargo/git/db/foo-abc123", true},
		{"cargo/git/checkouts/foo-abc123/def456/build.rs", "cargo/git/checkouts/foo-abc123", true},
		{"cargo/credentials.toml", "cargo/credentials.toml", false},
		{"gocache/aa/bb0123", "gocache/aa/bb0123", false},
		{"pnpm-store/v3/files/ab/cd", "pnpm-store/v3/files/ab/cd", false},
		{"unknown-section/sub/file", "unknown-section/sub", true},
		{"loose.bin", "loose.bin", false},
	}
	for _, c := range cases {
		got, dir := cacheEvictionUnit("/root", c.rel)
		if want := filepath.Join("/root", c.want); got != want || dir != c.dir {
			t.Errorf("cacheEvictionUnit(%q) = %q,%v want %q,%v", c.rel, got, dir, want, c.dir)
		}
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

func TestDiskCritical(t *testing.T) {
	cases := []struct {
		free, total int64
		pct         float64
		floor       int64
		want        bool
	}{
		{4 << 30, 100 << 30, 5, 2 << 30, true},  // 4% < 5%
		{1 << 30, 100 << 30, 0, 2 << 30, true},  // floor trips with pct off
		{4 << 30, 100 << 30, 0, 10 << 30, true}, // below floor alone
		{6 << 30, 100 << 30, 5, 2 << 30, false}, // inside both bounds
		{4 << 30, 100 << 30, 0, 0, false},       // fully disabled
	}
	for _, c := range cases {
		if got := diskCritical(c.free, c.total, c.pct, c.floor); got != c.want {
			t.Errorf("diskCritical(%d,%d,%v,%d) = %v, want %v", c.free, c.total, c.pct, c.floor, got, c.want)
		}
	}
}

// The critical tier runs the same sweeps at tighter horizons: 2h-old
// workspaces and a halved cache floor reclaim what the warn tier leaves.
func TestCriticalSweepHorizons(t *testing.T) {
	root := t.TempDir()
	recent := time.Now().Add(-3 * time.Hour) // stale for 2h, fresh for 24h
	mk := func(name string, mtime time.Time) {
		d := filepath.Join(root, name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(d, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	mk("job-dir", recent)

	a := &Agent{cfg: Config{WorkRoot: root}, log: zap.NewNop()}
	a.sweepWorkspaces() // warn-tier horizon keeps it
	if _, err := os.Stat(filepath.Join(root, "job-dir")); err != nil {
		t.Fatal("warn tier must keep a 3h-old workspace")
	}
	a.sweepWorkspacesOlderThan(criticalWorkspaceAge)
	if _, err := os.Stat(filepath.Join(root, "job-dir")); !os.IsNotExist(err) {
		t.Fatal("critical tier must sweep a 3h-old workspace")
	}
}

func TestJobActive(t *testing.T) {
	a := &Agent{}
	if a.jobActive("kern-svc-abc-svc") {
		t.Fatal("empty activeJobs must report inactive")
	}
	id := "123e4567-e89b-42d3-a456-426614174000"
	a.activeJobs.Store(id, struct{}{})
	for _, name := range []string{"kern-svc-" + id + "-postgres", "kern-job-" + id + "-1", "kern-svc-" + id} {
		if !a.jobActive(name) {
			t.Fatalf("expected active: %s", name)
		}
	}
	for _, name := range []string{"kern-svc-deadbeef-postgres", "kern-runner", "kern-svc-123e4567-e89b-42d3-a456-426614174001-postgres"} {
		if a.jobActive(name) {
			t.Fatalf("expected inactive: %s", name)
		}
	}
}

func TestSweepTmpStale(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	stale := filepath.Join(tmp, "pytest-of-root")
	fresh := filepath.Join(tmp, "trivy-scan-1")
	keep := filepath.Join(tmp, "gha-gocache")
	for _, d := range []string{stale, fresh, keep} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-48 * time.Hour)
	for _, d := range []string{stale, keep} {
		if err := os.Chtimes(d, old, old); err != nil {
			t.Fatal(err)
		}
	}
	a := &Agent{log: zap.NewNop()}
	a.sweepTmpStale(24 * time.Hour)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("stale pytest dir survived")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("fresh trivy dir removed")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("gha-gocache removed — operator path must stay")
	}
}
