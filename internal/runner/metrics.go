package runner

import (
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// heartbeatMetrics is the utilization snapshot sent with each heartbeat.
// Nil fields mean "could not measure" — the server stores them as NULL.
type heartbeatMetrics struct {
	CPUPercent    *float64 `json:"cpu_percent,omitempty"`
	MemUsedBytes  *int64   `json:"mem_used_bytes,omitempty"`
	DiskFreeBytes *int64   `json:"disk_free_bytes,omitempty"`
	ActiveJobs    int      `json:"active_jobs"`
}

// hostSampler derives utilization from /proc and statfs. CPU is a delta
// between consecutive samples; the first sample reports no percentage.
type hostSampler struct {
	prevBusy, prevTotal uint64
	hasPrev             bool
}

func (h *hostSampler) sample(workRoot string, activeJobs int) heartbeatMetrics {
	var m heartbeatMetrics
	m.ActiveJobs = activeJobs
	if busy, total, ok := cpuCounters(); ok {
		if h.hasPrev && total > h.prevTotal {
			v := float64(busy-h.prevBusy) / float64(total-h.prevTotal) * 100
			m.CPUPercent = &v
		}
		h.prevBusy, h.prevTotal, h.hasPrev = busy, total, true
	}
	if used, ok := memUsed(); ok {
		m.MemUsedBytes = &used
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(workRoot, &stat); err == nil {
		v := int64(stat.Bavail) * int64(stat.Bsize)
		m.DiskFreeBytes = &v
	}
	return m
}

// cpuCounters reads the aggregate "cpu" line of /proc/stat. busy is everything
// except idle+iowait.
func cpuCounters() (busy, total uint64, ok bool) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		var user, nice, system, idle, iowait, irq, softirq, steal uint64
		fields := strings.Fields(line[4:])
		vals := []*uint64{&user, &nice, &system, &idle, &iowait, &irq, &softirq, &steal}
		for i, f := range fields {
			if i >= len(vals) {
				break
			}
			*vals[i], _ = strconv.ParseUint(f, 10, 64)
		}
		total = user + nice + system + idle + iowait + irq + softirq + steal
		busy = total - idle - iowait
		return busy, total, true
	}
	return 0, 0, false
}

// memUsed reports total minus available from /proc/meminfo.
func memUsed() (int64, bool) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, false
	}
	var total, available int64
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		kb, _ := strconv.ParseInt(fields[1], 10, 64)
		switch fields[0] {
		case "MemTotal:":
			total = kb
		case "MemAvailable:":
			available = kb
		}
	}
	if total == 0 {
		return 0, false
	}
	return (total - available) * 1024, true
}
