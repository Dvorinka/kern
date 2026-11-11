package runner

import "testing"

// The per-GOOS probes must return sane values wherever they run — this is
// the only coverage the Windows/macOS paths get in CI, and it doubles as a
// smoke test when the compiled test binary is executed on those hosts.
func TestHostProbesReportSomething(t *testing.T) {
	if busy, total, ok := cpuCounters(); ok {
		if total == 0 || busy > total {
			t.Fatalf("cpuCounters = (%d, %d): busy exceeds total", busy, total)
		}
	}
	if total, avail, ok := memInfo(); ok {
		if total <= 0 {
			t.Fatalf("memInfo total = %d", total)
		}
		if avail < 0 || avail > total {
			t.Fatalf("memInfo available = %d out of total %d", avail, total)
		}
	}
	if used, ok := memUsed(); ok && used < 0 {
		t.Fatalf("memUsed = %d", used)
	}
	if n := cpusVisible(); n < 1 {
		t.Fatalf("cpusVisible = %d", n)
	}
	// loadAvg1 legitimately absent on Windows; where present it must be
	// non-negative.
	if v, ok := loadAvg1(); ok && v < 0 {
		t.Fatalf("loadAvg1 = %v", v)
	}
}
