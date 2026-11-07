// Package telemetry holds process-local metrics exported on /metrics
// alongside the store-backed series. Counters and gauges are exact; the
// HTTP latency histogram uses fixed buckets. All state is per-process —
// multi-replica deployments should scrape each replica and aggregate in
// Prometheus, which is the standard model anyway.
package telemetry

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
)

type metricMeta struct {
	help    string
	kind    string // counter | gauge | histogram
	buckets []float64
}

type histogram struct {
	buckets []float64
	counts  []uint64
	sum     float64
	count   uint64
}

// Registry is a tiny Prometheus-text metrics store.
type Registry struct {
	mu     sync.Mutex
	meta   map[string]metricMeta
	floats map[string]float64 // counters and gauges share the map; kind comes from meta
	hists  map[string]*histogram
}

// Default is the process-wide registry wired into /metrics.
var Default = &Registry{
	meta:   map[string]metricMeta{},
	floats: map[string]float64{},
	hists:  map[string]*histogram{},
}

func labelKey(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `%s=%q`, k, labels[k])
	}
	b.WriteByte('}')
	return b.String()
}

func (r *Registry) floatMetric(name, help, kind string, labels map[string]string) string {
	key := name + labelKey(labels)
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.meta[name]; !ok {
		r.meta[name] = metricMeta{help: help, kind: kind}
	}
	return key
}

// IncCounter adds 1 to a counter series.
func (r *Registry) IncCounter(name, help string, labels map[string]string) {
	key := r.floatMetric(name, help, "counter", labels)
	r.mu.Lock()
	r.floats[key]++
	r.mu.Unlock()
}

// AddCounter adds v to a counter series.
func (r *Registry) AddCounter(name, help string, labels map[string]string, v float64) {
	key := r.floatMetric(name, help, "counter", labels)
	r.mu.Lock()
	r.floats[key] += v
	r.mu.Unlock()
}

// SetGauge assigns a gauge series.
func (r *Registry) SetGauge(name, help string, labels map[string]string, v float64) {
	key := r.floatMetric(name, help, "gauge", labels)
	r.mu.Lock()
	r.floats[key] = v
	r.mu.Unlock()
}

// Observe records one histogram sample into fixed buckets.
func (r *Registry) Observe(name, help string, buckets []float64, labels map[string]string, v float64) {
	key := name + labelKey(labels)
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.meta[name]
	if !ok {
		m = metricMeta{help: help, kind: "histogram", buckets: buckets}
		r.meta[name] = m
	}
	h := r.hists[key]
	if h == nil {
		h = &histogram{buckets: m.buckets, counts: make([]uint64, len(m.buckets)+1)}
		r.hists[key] = h
	}
	h.count++
	h.sum += v
	// counts[i] is cumulative: observations <= buckets[i]; the last slot is +Inf.
	for i, b := range h.buckets {
		if v <= b {
			for j := i; j < len(h.counts); j++ {
				h.counts[j]++
			}
			return
		}
	}
	h.counts[len(h.counts)-1]++
}

// Exposition renders every series in Prometheus text format 0.0.4.
func (r *Registry) Exposition() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	emitted := map[string]bool{}
	for key, v := range r.floats {
		name := metricName(key)
		m := r.meta[name]
		if !emitted[name] {
			emitted[name] = true
			fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", name, m.help, name, m.kind)
		}
		fmt.Fprintf(&b, "%s %g\n", key, v)
	}
	for key, h := range r.hists {
		name := metricName(key)
		m := r.meta[name]
		if !emitted[name] {
			emitted[name] = true
			fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s histogram\n", name, m.help, name)
		}
		suffix := key[len(name):] // "" or "{labels}"
		for i, bound := range h.buckets {
			fmt.Fprintf(&b, "%s_bucket%s %d\n", name, withLE(suffix, bound), h.counts[i])
		}
		fmt.Fprintf(&b, "%s_bucket%s %d\n", name, withLE(suffix, math.Inf(1)), h.counts[len(h.counts)-1])
		fmt.Fprintf(&b, "%s_sum%s %g\n%s_count%s %d\n", name, suffix, h.sum, name, suffix, h.count)
	}
	return b.String()
}

func metricName(key string) string {
	if i := strings.IndexByte(key, '{'); i >= 0 {
		return key[:i]
	}
	return key
}

// withLE returns the label suffix with an le="bound" label merged in.
func withLE(suffix string, bound float64) string {
	le := fmt.Sprintf(`,le=%q`, fmt.Sprintf("%g", bound))
	if suffix == "" {
		return "{" + strings.TrimPrefix(le, ",") + "}"
	}
	return strings.TrimSuffix(suffix, "}") + le + "}"
}

// ---- named instruments ----

var httpBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// ObserveHTTPRequest records one API request's latency.
func ObserveHTTPRequest(method, route, statusClass string, seconds float64) {
	Default.Observe("kern_http_request_duration_seconds",
		"HTTP request latency by route", httpBuckets,
		map[string]string{"method": method, "route": route, "status": statusClass}, seconds)
}

// IncGitHubAPI counts one GitHub API call by outcome.
func IncGitHubAPI(result string) {
	Default.IncCounter("kern_github_api_requests_total",
		"GitHub API requests by result", map[string]string{"result": result})
}

// SetGitHubRateLimit publishes the last seen X-RateLimit-Remaining value.
func SetGitHubRateLimit(remaining int) {
	Default.SetGauge("kern_github_rate_limit_remaining",
		"GitHub API rate limit remaining (last response)", nil, float64(remaining))
}

// SetSchedulerLeader reports whether this replica holds the tick lock.
func SetSchedulerLeader(leading bool) {
	v := 0.0
	if leading {
		v = 1
	}
	Default.SetGauge("kern_scheduler_leader",
		"1 when this replica holds the scheduler advisory lock", nil, v)
}

// IncNotify counts notification delivery outcomes.
func IncNotify(result string) {
	Default.IncCounter("kern_notify_deliveries_total",
		"Outbound notification deliveries by result", map[string]string{"result": result})
}

// ObserveSchedulerTick records one orchestrator tick's duration.
func ObserveSchedulerTick(seconds float64) {
	Default.Observe("kern_scheduler_tick_duration_seconds",
		"Orchestrator tick duration", httpBuckets, nil, seconds)
}
