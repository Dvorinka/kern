package runner

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestEgressAllowlistSuffixes(t *testing.T) {
	al := newEgressAllowlist([]string{"github.com", "npmjs.org", ".pypi.org"})
	for host, want := range map[string]bool{
		"github.com":             true,
		"api.github.com":         true,
		"codeload.github.com":    true,
		"GITHUB.COM":             true,
		"registry.npmjs.org":     true,
		"pypi.org":               true,
		"files.pythonhosted.org": false, // pythonhosted.org is a separate suffix
		"notgithub.com":          false,
		"github.com.evil.com":    false,
		"evilnpmjs.org":          false,
		"":                       false,
	} {
		if got := al.allows(host); got != want {
			t.Errorf("allows(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestEgressProxyForwardAllowDeny(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("upstream-ok"))
	}))
	defer upstream.Close()
	// The test upstream listens on an ephemeral port; grant it through the
	// port gate for the duration of the test.
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(upstream.URL, "http://"))
	egressPorts[port] = true
	defer delete(egressPorts, port)

	al := newEgressAllowlist([]string{"127.0.0.1", "allowed.example"})
	proxy := httptest.NewServer(egressHandler(al))
	defer proxy.Close()

	proxyURL, _ := url.Parse(proxy.URL)
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		Timeout:   20 * time.Second,
	}

	// Allowed host end-to-end: the request tunnels through the proxy to
	// the upstream and returns its status and body.
	resp, err := client.Get(upstream.URL + "/x")
	if err != nil {
		t.Fatalf("allowed request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusTeapot || string(body) != "upstream-ok" {
		t.Fatalf("allowed response = %d %q, want 418 upstream-ok", resp.StatusCode, body)
	}

	// Denied host: 403 before any dial.
	resp, err = client.Get("http://denied.example/x")
	if err != nil {
		t.Fatalf("denied request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("denied host status = %d, want 403", resp.StatusCode)
	}

	// Allowed name but non-allowlisted port: denied at the port gate.
	resp, err = client.Get("http://127.0.0.1:1/x")
	if err != nil {
		t.Fatalf("bad-port request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("bad-port status = %d, want 403", resp.StatusCode)
	}
}

func TestEgressProxyConnectPolicy(t *testing.T) {
	al := newEgressAllowlist([]string{"github.com"})
	proxy := httptest.NewServer(egressHandler(al))
	defer proxy.Close()

	for _, target := range []string{"evil.com:443", "github.com:22", "github.com:80"} {
		status := connectStatus(t, proxy.URL, target)
		if !strings.Contains(status, "403") {
			t.Fatalf("CONNECT %s status = %q, want 403", target, status)
		}
	}
}

// connectStatus issues a raw CONNECT through the proxy and returns the
// status line. Only denial paths are exercised — an allowed CONNECT would
// need a real :443 upstream.
func connectStatus(t *testing.T, proxyURL, target string) string {
	t.Helper()
	host := strings.TrimPrefix(proxyURL, "http://")
	conn, err := net.DialTimeout("tcp", host, 5*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target); err != nil {
		t.Fatalf("write CONNECT: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	status, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	return status
}
