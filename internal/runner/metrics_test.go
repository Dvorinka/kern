package runner

import (
	"strings"
	"testing"
)

func TestParseNetIO(t *testing.T) {
	rx, tx := parseNetIO("1.5MiB / 2GiB")
	if rx != 1572864 || tx != 2147483648 {
		t.Fatalf("parseNetIO = %d, %d", rx, tx)
	}
	rx, tx = parseNetIO("0B / 0B")
	if rx != 0 || tx != 0 {
		t.Fatalf("zero parse = %d, %d", rx, tx)
	}
	if rx, tx := parseNetIO("garbage"); rx != 0 || tx != 0 {
		t.Fatalf("bad parse = %d, %d", rx, tx)
	}
}

func TestJobMetricsEgress(t *testing.T) {
	m := &jobMetrics{}
	m.observeEgress(100, 250)
	m.observeEgress(90, 200) // stale sample must not shrink the total
	if m.EgressBytes != 250 {
		t.Fatalf("egress = %d, want 250", m.EgressBytes)
	}
	raw := m.json()
	if raw == nil || !strings.Contains(string(raw), `"egress_bytes":250`) {
		t.Fatalf("json = %s", raw)
	}
	// Egress alone — no cpu/mem sample — still emits a document.
	e := &jobMetrics{}
	e.observeEgress(0, 42)
	if e.json() == nil {
		t.Fatal("egress-only metrics should serialize")
	}
}
