package runner

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// heartbeatMetrics is the utilization snapshot sent with each heartbeat.
// Nil fields mean "could not measure" — the server stores them as NULL.
type heartbeatMetrics struct {
	CPUPercent    *float64 `json:"cpu_percent,omitempty"`
	MemUsedBytes  *int64   `json:"mem_used_bytes,omitempty"`
	DiskFreeBytes *int64   `json:"disk_free_bytes,omitempty"`
	ActiveJobs    int      `json:"active_jobs"`
	SelfUpdate    bool     `json:"self_update,omitempty"`
	// Deregister asks the control plane to delete this runner's row — the
	// ephemeral-runner exit path, refused server-side while jobs are in flight.
	Deregister bool `json:"deregister,omitempty"`
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
	if free, _ := diskSpace(workRoot); free > 0 {
		m.DiskFreeBytes = &free
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

// loadAvg1 reads the 1-minute load average, used by the optional claim gate.
func loadAvg1() (float64, bool) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.Fields(string(data))[0], 64)
	return v, err == nil
}

// cpusVisible counts the processors the kernel reports via /proc/cpuinfo.
// That is the machine /proc/loadavg describes — inside an LXC or a cpuset
// docker container, runtime.NumCPU reflects the container's quota while
// loadavg stays host-wide, so normalizing by NumCPU over-fires the gate.
func cpusVisible() int {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err == nil {
		n := strings.Count(string(data), "\nprocessor")
		if strings.HasPrefix(string(data), "processor") {
			n++
		}
		if n > 0 {
			return n
		}
	}
	return runtime.NumCPU()
}

// memUsed reports total minus available from /proc/meminfo.
func memUsed() (int64, bool) {
	total, available, ok := memInfo()
	if !ok {
		return 0, false
	}
	return (total - available) * 1024, true
}

// memAvailable reports MemAvailable from /proc/meminfo — the kernel's
// estimate of memory a new allocation can use without swapping.
func memAvailable() (int64, bool) {
	_, available, ok := memInfo()
	return available * 1024, ok
}

func memInfo() (total, available int64, ok bool) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, false
	}
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
	return total, available, total > 0
}

// --- per-job container metrics (#134) ---------------------------------------
//
// While a docker job runs, a goroutine polls `docker stats --no-stream`
// every 15s and keeps the peaks; the JSON lands on jobs.metrics via the
// finishJob payload. Host executors have no container — the field stays empty.

type jobMetrics struct {
	mu           sync.Mutex
	CPUPeakPct   float64 `json:"cpu_pct_peak,omitempty"`
	MemPeakBytes int64   `json:"mem_bytes_peak,omitempty"`
}

func (m *jobMetrics) observe(cpu float64, mem int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cpu > m.CPUPeakPct {
		m.CPUPeakPct = cpu
	}
	if mem > m.MemPeakBytes {
		m.MemPeakBytes = mem
	}
}

// json returns the metrics document, or nil when nothing was sampled.
func (m *jobMetrics) json() json.RawMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.CPUPeakPct == 0 && m.MemPeakBytes == 0 {
		return nil
	}
	b, _ := json.Marshal(m)
	return b
}

// startMetricsSampler polls docker stats for the job container until ctx
// ends. First sample is immediate; misses are ignored (container may not be
// up yet, or already gone at teardown).
func (e *executor) startMetricsSampler(ctx context.Context) {
	if e.host {
		return
	}
	m := &jobMetrics{}
	e.metrics = m
	sample := func() {
		out, err := exec.CommandContext(ctx, "docker", "stats", "--no-stream",
			"--format", "{{.CPUPerc}} {{.MemUsage}}", e.container).Output()
		if err != nil {
			return
		}
		cpu, mem := parseDockerStats(string(out))
		m.observe(cpu, mem)
	}
	sample()
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				sample()
			}
		}
	}()
}

// parseDockerStats reads "12.34% 123.4MiB / 1GiB" into (12.34, 129394278).
func parseDockerStats(s string) (float64, int64) {
	fields := strings.Fields(s)
	if len(fields) < 2 {
		return 0, 0
	}
	cpu, _ := strconv.ParseFloat(strings.TrimSuffix(fields[0], "%"), 64)
	return cpu, parseHumanBytes(fields[1])
}

var humanScale = map[string]float64{
	"B": 1, "KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30, "TiB": 1 << 40,
	"kB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12,
}

func parseHumanBytes(s string) int64 {
	i := 0
	for i < len(s) && (s[i] == '.' || (s[i] >= '0' && s[i] <= '9')) {
		i++
	}
	v, _ := strconv.ParseFloat(s[:i], 64)
	return int64(v * humanScale[s[i:]])
}
