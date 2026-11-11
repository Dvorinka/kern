package runner

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestPublishSpec(t *testing.T) {
	cases := map[string]string{
		"5432":        "5432:5432",
		"5433:5432":   "5433:5432",
		"8080:80/tcp": "8080:80/tcp",
		"53:53/udp":   "53:53/udp",
		"53/udp":      "53:53/udp",
	}
	for in, want := range cases {
		if got := publishSpec(in); got != want {
			t.Errorf("publishSpec(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPortSpecParts(t *testing.T) {
	cases := []struct {
		in         string
		host, cont string
		proto      string
	}{
		{"5432", "5432", "5432", ""},
		{"5433:5432", "5433", "5432", ""},
		{"8080:80/tcp", "8080", "80", "/tcp"},
		{"53/udp", "53", "53", "/udp"},
		{"5353:53/udp", "5353", "53", "/udp"},
	}
	for _, c := range cases {
		h, k, p := portSpecParts(c.in)
		if h != c.host || k != c.cont || p != c.proto {
			t.Errorf("portSpecParts(%q) = %q,%q,%q want %q,%q,%q", c.in, h, k, p, c.host, c.cont, c.proto)
		}
	}
}

func TestPublishLockKey(t *testing.T) {
	cases := map[string]string{
		"5432":        "127.0.0.1:5432",
		"5433:5432":   "127.0.0.1:5433",
		"8080:80/tcp": "127.0.0.1:8080",
		"8080:80":     "127.0.0.1:8080",
		"53:53/udp":   "127.0.0.1:53/udp",
		"53/udp":      "127.0.0.1:53/udp",
	}
	for in, want := range cases {
		if got := publishLockKey(in); got != want {
			t.Errorf("publishLockKey(%q) = %q, want %q", in, got, want)
		}
	}
	// tcp and bare share a socket — same key.
	if publishLockKey("8080:80") != publishLockKey("8080:80/tcp") {
		t.Error("tcp-suffixed and bare specs must share a lock key")
	}
	// udp is a different socket — different key.
	if publishLockKey("53:53/udp") == publishLockKey("53:53") {
		t.Error("udp and tcp specs must not share a lock key")
	}
}

func trustedExecutor(ports []string) *executor {
	e := &executor{claim: &Claim{}, log: zap.NewNop()}
	e.claim.Run.TrustLevel = "trusted"
	e.claim.Job.Services = []ClaimService{{Name: "svc", Ports: ports}}
	return e
}

func TestServiceArgsHostExecutor(t *testing.T) {
	e := &executor{claim: &Claim{}, log: zap.NewNop(), host: true, container: ""}
	e.claim.Run.TrustLevel = "trusted"
	e.claim.Job.ID = "job-1"

	args := e.serviceArgs("kern-svc-net", ClaimService{Name: "pg", Image: "postgres:16", Ports: []string{"5433:5432"}})
	joined := strings.Join(args, " ")
	for _, want := range []string{"--network kern-svc-net", "--network-alias pg", "-p 127.0.0.1:5433:5432"} {
		if !strings.Contains(joined, want) {
			t.Errorf("host service args missing %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "--network container:") {
		t.Errorf("host executor must never share a container netns: %s", joined)
	}
	// A port-less service joins the bridge for sibling DNS only — no -p.
	args = e.serviceArgs("kern-svc-net", ClaimService{Name: "redis", Image: "redis:7"})
	joined = strings.Join(args, " ")
	if strings.Contains(joined, "-p ") {
		t.Errorf("undeclared service must not publish: %s", joined)
	}
	if !strings.Contains(joined, "--network-alias redis") {
		t.Errorf("undeclared service still needs its alias: %s", joined)
	}
}

func TestAcquireServicePortsSerializes(t *testing.T) {
	e1 := trustedExecutor([]string{"5432:5432"})
	e2 := trustedExecutor([]string{"5432:5432"})
	ctx := context.Background()

	if err := e1.acquireServicePorts(ctx); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- e2.acquireServicePorts(ctx) }()

	select {
	case err := <-done:
		t.Fatalf("second acquire should block, got %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	e1.releaseServicePorts()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("second acquire after release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second acquire did not proceed after release")
	}
	e2.releaseServicePorts()
}

func TestAcquireServicePortsDifferentPorts(t *testing.T) {
	e1 := trustedExecutor([]string{"5432:5432"})
	e2 := trustedExecutor([]string{"5433:5432"})
	ctx := context.Background()
	if err := e1.acquireServicePorts(ctx); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer e1.releaseServicePorts()
	done := make(chan error, 1)
	go func() { done <- e2.acquireServicePorts(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("different port should not block: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("different host port blocked unexpectedly")
	}
	e2.releaseServicePorts()
}

func TestAcquireServicePortsUnprivilegedSkips(t *testing.T) {
	e := &executor{claim: &Claim{}, log: zap.NewNop()}
	e.claim.Job.Services = []ClaimService{{Name: "svc", Ports: []string{"5432:5432"}}}
	if err := e.acquireServicePorts(context.Background()); err != nil {
		t.Fatalf("unprivileged acquire: %v", err)
	}
	if len(e.portReleases) != 0 {
		t.Fatal("unprivileged job must not take port holds")
	}
}

func TestAcquireServicePortsCancelReleasesPartial(t *testing.T) {
	e1 := trustedExecutor([]string{"6001:6001", "6002:6002"})
	e2 := trustedExecutor([]string{"6001:6001", "6002:6002"})
	ctx := context.Background()
	if err := e1.acquireServicePorts(ctx); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer e1.releaseServicePorts()

	// e2 blocks on 6001; cancelling must free nothing it never held and
	// leave e1's holds intact.
	cctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- e2.acquireServicePorts(cctx) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled acquire should return an error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled acquire did not return")
	}
	if len(e2.portReleases) != 0 {
		t.Fatal("cancelled acquire leaked port holds")
	}
}
