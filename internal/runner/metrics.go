package runner

import (
	"context"
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dvorinka/kern/internal/version"
)

// heartbeatMetrics is the utilization snapshot sent with each heartbeat.
// Nil fields mean "could not measure" — the server stores them as NULL.
type heartbeatMetrics struct {
	CPUPercent    *float64 `json:"cpu_percent,omitempty"`
	MemUsedBytes  *int64   `json:"mem_used_bytes,omitempty"`
	DiskFreeBytes *int64   `json:"disk_free_bytes,omitempty"`
	// DiskBytes is the work-root filesystem total — refresh on heartbeat so
	// a moved/resized work root stops reporting register-time capacity.
	DiskBytes *int64 `json:"disk_bytes,omitempty"`
	// ClaimsPaused reports the live claim-gate state — comma-joined engaged
	// gates (disk,memory,overload), '' while claiming — so a paused runner
	// stops reading as merely idle on the dashboard.
	ClaimsPaused string `json:"claims_paused,omitempty"`
	// Labels resyncs the runner's advertised label set every heartbeat —
	// token-mode runners never re-register, so without it a runner enrolled
	// before name-labels keeps a stale set forever and each_runner pinning
	// silently degrades to its one unpinned copy.
	Labels []string `json:"labels,omitempty"`
	// Version refreshes runners.version every heartbeat. Without it a
	// token-mode runner (no re-register on boot) reports its enroll-time
	// version forever, and the auto-update offer never converges.
	Version    string `json:"version,omitempty"`
	ActiveJobs int    `json:"active_jobs"`
	SelfUpdate bool   `json:"self_update,omitempty"`
	// Deregister asks the control plane to delete this runner's row — the
	// ephemeral-runner exit path, refused server-side while jobs are in flight.
	Deregister bool `json:"deregister,omitempty"`
}

// hostSampler derives utilization from OS counters. CPU is a delta
// between consecutive samples; the first sample reports no percentage.
// The probes themselves are per-GOOS (metrics_linux.go, metrics_darwin.go,
// metrics_windows.go) — a missing probe reports nil fields, which the
// server stores as NULL.
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
	if free, total := diskSpace(workRoot); free > 0 {
		m.DiskFreeBytes = &free
		m.DiskBytes = &total
	}
	m.Version = version.Version
	return m
}

// memUsed reports total minus available memory.
func memUsed() (int64, bool) {
	total, available, ok := memInfo()
	if !ok {
		return 0, false
	}
	return (total - available), true
}

// memAvailable reports the kernel's estimate of memory a new allocation
// can use without swapping.
func memAvailable() (int64, bool) {
	_, available, ok := memInfo()
	return available, ok
}

// --- per-job container metrics (#134) ---------------------------------------
//
// While a docker job runs, a goroutine polls `docker stats --no-stream`
// every 15s and keeps the peaks; the JSON lands on jobs.metrics via the
// finishJob payload. Host executors have no container — the field stays empty.

type jobMetrics struct {
	mu            sync.Mutex
	CPUPeakPct    float64 `json:"cpu_pct_peak,omitempty"`
	MemPeakBytes  int64   `json:"mem_bytes_peak,omitempty"`
	DiskPeakBytes int64   `json:"disk_bytes_peak,omitempty"`
	// EgressBytes totals the fork-tier proxy's NetIO. Every proxied byte
	// crosses the proxy twice (job leg + internet leg), so RX and TX both
	// approximate the real total; the larger wins. Untrusted traffic is the
	// only traffic metered — trusted jobs have no proxy.
	EgressBytes int64 `json:"egress_bytes,omitempty"`
}

func (m *jobMetrics) observeDisk(b int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b > m.DiskPeakBytes {
		m.DiskPeakBytes = b
	}
}

func (m *jobMetrics) observeEgress(rx, tx int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v := max(rx, tx); v > m.EgressBytes {
		m.EgressBytes = v
	}
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
	if m.CPUPeakPct == 0 && m.MemPeakBytes == 0 && m.EgressBytes == 0 {
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
	proxy := ""
	if e.egressRestricted() {
		proxy = e.egressProxyName()
	}
	sample := func() {
		out, err := exec.CommandContext(ctx, "docker", "stats", "--no-stream",
			"--format", "{{.CPUPerc}} {{.MemUsage}}", e.container).Output()
		if err != nil {
			return
		}
		cpu, mem := parseDockerStats(string(out))
		m.observe(cpu, mem)
		if proxy != "" {
			if pout, perr := exec.CommandContext(ctx, "docker", "stats", "--no-stream",
				"--format", "{{.NetIO}}", proxy).Output(); perr == nil {
				m.observeEgress(parseNetIO(string(pout)))
			}
		}
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

// parseNetIO reads docker's "1.23MiB / 4.56MiB" cumulative network counters
// into (rx, tx) bytes.
func parseNetIO(s string) (int64, int64) {
	parts := strings.Split(s, "/")
	if len(parts) != 2 {
		return 0, 0
	}
	return parseHumanBytes(strings.TrimSpace(parts[0])),
		parseHumanBytes(strings.TrimSpace(parts[1]))
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
