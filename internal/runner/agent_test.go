package runner

import (
	"testing"

	"go.uber.org/zap"
)

func TestMemClaimPaused(t *testing.T) {
	defer func(f func() (int64, bool)) { memAvailableFn = f }(memAvailableFn)
	avail := int64(4 << 30)
	memAvailableFn = func() (int64, bool) { return avail, true }
	a := &Agent{cfg: Config{MemMinFreeBytes: 1 << 30}, log: zap.NewNop()}
	if a.memClaimPaused() {
		t.Fatal("paused with 4 GiB available against a 1 GiB floor")
	}
	avail = 512 << 20
	if !a.memClaimPaused() {
		t.Fatal("not paused under the floor")
	}
	avail = 2 << 30
	if a.memClaimPaused() {
		t.Fatal("still paused after recovery")
	}
	memAvailableFn = func() (int64, bool) { return 0, false }
	if a.memClaimPaused() {
		t.Fatal("paused on an unreadable probe")
	}
	a.cfg.MemMinFreeBytes = 0
	avail = 1
	memAvailableFn = func() (int64, bool) { return 1, true }
	if a.memClaimPaused() {
		t.Fatal("gate disabled but still pausing")
	}
}

func TestOverLoadedPSIGate(t *testing.T) {
	defer func(p func() (float64, bool), l func() (float64, bool), c func() int) {
		cgroupPressureFn, loadAvg1Fn, cpusVisibleFn = p, l, c
	}(cgroupPressureFn, loadAvg1Fn, cpusVisibleFn)
	loadAvg1Fn = func() (float64, bool) { return 999, true }
	cpusVisibleFn = func() int { return 1 }
	a := &Agent{cfg: Config{PsiGate: 25, MaxLoadPerCPU: 2}, log: zap.NewNop()}

	// PSI readable replaces loadavg: huge host load, quiet container, no pause.
	cgroupPressureFn = func() (float64, bool) { return 5, true }
	if a.overLoaded() {
		t.Fatal("psi 5 under gate 25 should not pause despite saturated loadavg")
	}
	cgroupPressureFn = func() (float64, bool) { return 40, true }
	if !a.overLoaded() {
		t.Fatal("psi 40 over gate 25 should pause")
	}
	// PSI unreadable falls back to the loadavg gate.
	cgroupPressureFn = func() (float64, bool) { return 0, false }
	if !a.overLoaded() {
		t.Fatal("psi unavailable should fall back to loadavg gate")
	}
}

func TestOverLoadedUsesVisibleCPUs(t *testing.T) {
	defer func(l func() (float64, bool), c func() int) {
		loadAvg1Fn, cpusVisibleFn = l, c
	}(loadAvg1Fn, cpusVisibleFn)
	loadAvg1Fn = func() (float64, bool) { return 30, true }
	a := &Agent{cfg: Config{MaxLoadPerCPU: 2}, log: zap.NewNop()}
	cpusVisibleFn = func() int { return 6 } // LXC cpuset — old behaviour, gated
	if !a.overLoaded() {
		t.Fatal("load 30 over 2x6=12 should gate")
	}
	cpusVisibleFn = func() int { return 20 } // host view from /proc/cpuinfo
	if a.overLoaded() {
		t.Fatal("load 30 under 2x20=40 should not gate")
	}
}
