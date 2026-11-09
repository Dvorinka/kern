package runner

import (
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestDiskClaimPausedTransitions(t *testing.T) {
	var free int64
	old := diskSpaceFn
	diskSpaceFn = func(string) (int64, int64) { return free, 100 << 30 }
	defer func() { diskSpaceFn = old }()

	a := &Agent{log: zap.NewNop()}
	a.cfg.WorkRoot = t.TempDir()
	a.cfg.DiskMinFreePct = 10

	free = 50 << 30 // 50% free — claims allowed
	if a.diskClaimPaused() {
		t.Fatal("expected claims allowed above the threshold")
	}
	free = 5 << 30 // 5% free — under pressure
	if !a.diskClaimPaused() {
		t.Fatal("expected claims paused under the threshold")
	}
	if !a.diskPaused.Load() {
		t.Fatal("pause should latch")
	}
	free = 50 << 30
	if a.diskClaimPaused() {
		t.Fatal("expected claims to resume after pressure clears")
	}
	if a.diskPaused.Load() {
		t.Fatal("latch should clear")
	}
}

func TestDiskClaimPausedDisabled(t *testing.T) {
	old := diskSpaceFn
	diskSpaceFn = func(string) (int64, int64) { return 1, 100 << 30 }
	defer func() { diskSpaceFn = old }()

	a := &Agent{log: zap.NewNop()}
	a.cfg.WorkRoot = t.TempDir()
	// DiskMinFreePct zero disables the gate entirely.
	if a.diskClaimPaused() {
		t.Fatal("disabled gate must not pause claims")
	}
}

func TestJobErrorHint(t *testing.T) {
	old := diskSpaceFn
	diskSpaceFn = func(string) (int64, int64) { return 512 << 20, 80 << 30 }
	defer func() { diskSpaceFn = old }()

	raw := "checkout: write /opt/kern/work/x: no space left on device"
	got := jobErrorHint(raw, "/unused")
	if !strings.Contains(got, "runner disk full") || !strings.Contains(got, "512 MiB free") {
		t.Fatalf("expected disk hint appended, got %q", got)
	}
	if got := jobErrorHint("checkout: connection refused", "/unused"); got != "checkout: connection refused" {
		t.Fatalf("unrelated errors must pass through, got %q", got)
	}
}
