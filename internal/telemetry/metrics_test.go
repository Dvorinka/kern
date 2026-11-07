package telemetry

import (
	"strings"
	"testing"
)

func TestExpositionCountersGaugesHistograms(t *testing.T) {
	r := &Registry{meta: map[string]metricMeta{}, floats: map[string]float64{}, hists: map[string]*histogram{}}
	r.IncCounter("test_requests_total", "requests", map[string]string{"route": "/a"})
	r.IncCounter("test_requests_total", "requests", map[string]string{"route": "/a"})
	r.SetGauge("test_queue", "queued", nil, 3)
	r.Observe("test_latency_seconds", "latency", []float64{0.1, 1}, map[string]string{"route": "/a"}, 0.05)
	r.Observe("test_latency_seconds", "latency", []float64{0.1, 1}, map[string]string{"route": "/a"}, 5)

	out := r.Exposition()
	for _, want := range []string{
		`test_requests_total{route="/a"} 2`,
		`test_queue 3`,
		`test_latency_seconds_bucket{route="/a",le="0.1"} 1`,
		`test_latency_seconds_bucket{route="/a",le="1"} 1`,
		`test_latency_seconds_bucket{route="/a",le="+Inf"} 2`,
		`test_latency_seconds_sum{route="/a"} 5.05`,
		`test_latency_seconds_count{route="/a"} 2`,
		"# TYPE test_latency_seconds histogram",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("exposition missing %q\n---\n%s", want, out)
		}
	}
}
