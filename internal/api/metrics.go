package api

// In-process metrics for /metrics. The store-backed series (queue wait, step
// duration, runner gauges) are computed at scrape time; these cover signals
// that only exist inside the API process: claim query latency, webhook
// processing and cache lookup outcomes. Scheduler tick cost lives in
// internal/telemetry (leader-gated). Counters reset on restart — Prometheus
// rate() handles that; histograms use fixed buckets so exposition stays cheap.

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dvorinka/kern/internal/store"
)

// histogram is a fixed-bucket duration histogram.
type histogram struct {
	buckets []float64
	counts  []uint64 // len(buckets)+1, last bucket is +Inf
	sum     float64
	count   uint64
}

func newHistogram(buckets []float64) *histogram {
	sorted := append([]float64(nil), buckets...)
	sort.Float64s(sorted)
	return &histogram{buckets: sorted, counts: make([]uint64, len(sorted)+1)}
}

func (h *histogram) observe(v float64) {
	i := sort.Search(len(h.buckets), func(i int) bool { return v <= h.buckets[i] })
	h.counts[i]++
	h.sum += v
	h.count++
}

// write emits Prometheus histogram lines for name.
func (h *histogram) write(b *strings.Builder, name, help string) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s histogram\n", name, help, name)
	var cumulative uint64
	for i, edge := range h.buckets {
		cumulative += h.counts[i]
		fmt.Fprintf(b, "%s_bucket{le=\"%g\"} %d\n", name, edge, cumulative)
	}
	cumulative += h.counts[len(h.counts)-1]
	fmt.Fprintf(b, "%s_bucket{le=\"+Inf\"} %d\n", name, cumulative)
	fmt.Fprintf(b, "%s_sum %g\n%s_count %d\n", name, h.sum, name, h.count)
}

type kernMetrics struct {
	mu             sync.Mutex
	cacheLookups   map[string]uint64
	claimQuery     *histogram
	webhookProcess *histogram
}

func newKernMetrics() *kernMetrics {
	return &kernMetrics{
		cacheLookups: map[string]uint64{},
		// ClaimJob runs every 500ms per idle worker; p99 should stay well
		// under a second even with a deep queue.
		claimQuery: newHistogram([]float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}),
		// Webhook processing includes run creation and GitHub API calls.
		webhookProcess: newHistogram([]float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}),
	}
}

func (m *kernMetrics) observeCache(result string) {
	m.mu.Lock()
	m.cacheLookups[result]++
	m.mu.Unlock()
}

func (m *kernMetrics) observeClaimQuery(d time.Duration) {
	m.mu.Lock()
	m.claimQuery.observe(d.Seconds())
	m.mu.Unlock()
}

func (m *kernMetrics) observeWebhook(d time.Duration) {
	m.mu.Lock()
	m.webhookProcess.observe(d.Seconds())
	m.mu.Unlock()
}

// snapshot returns a stable copy for rendering.
func (m *kernMetrics) snapshot() (map[string]uint64, *histogram, *histogram) {
	m.mu.Lock()
	defer m.mu.Unlock()
	lookups := make(map[string]uint64, len(m.cacheLookups))
	for k, v := range m.cacheLookups {
		lookups[k] = v
	}
	return lookups, m.claimQuery, m.webhookProcess
}

// writeDurationHistogram emits a store.DurationHistogram (non-cumulative
// buckets) as a Prometheus histogram with cumulative le buckets.
func writeDurationHistogram(b *strings.Builder, name, help string, h store.DurationHistogram) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s histogram\n", name, help, name)
	var cumulative uint64
	for i, edge := range h.Buckets {
		cumulative += h.Counts[i]
		fmt.Fprintf(b, "%s_bucket{le=\"%g\"} %d\n", name, edge, cumulative)
	}
	cumulative += h.Counts[len(h.Buckets)]
	fmt.Fprintf(b, "%s_bucket{le=\"+Inf\"} %d\n", name, cumulative)
	fmt.Fprintf(b, "%s_sum %g\n%s_count %d\n", name, h.Sum, name, h.Count)
}

// labelValue escapes a Prometheus label value.
func labelValue(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	return strings.ReplaceAll(v, `"`, `\"`)
}
