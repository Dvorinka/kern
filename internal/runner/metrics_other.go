//go:build !linux && !darwin && !windows

package runner

// Stubs for platforms without a probe implementation: every metric reports
// "could not measure" and the server stores NULL.

func cpuCounters() (busy, total uint64, ok bool) { return 0, 0, false }

func loadAvg1() (float64, bool) { return 0, false }

func cpusVisible() int { return 1 }

func memInfo() (total, available int64, ok bool) { return 0, 0, false }
