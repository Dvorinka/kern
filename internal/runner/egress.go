package runner

// Egress allowlist for untrusted-tier jobs. Fork-trust jobs run on a per-job
// `--internal` docker network: no external route exists, so every packet must
// go through a sibling proxy container that enforces a domain allowlist. The
// proxy is this same binary (`kern-runner egress-proxy`) packaged into a
// minimal image the runner builds once — scratch for the static shipped
// binaries, a local busybox/alpine for dynamic dev builds; no required
// external image pull, no host iptables, and failure is closed: a missing
// proxy means no egress.
//
// Coverage is HTTP/S only. Tools that ignore HTTP_PROXY/HTTPS_PROXY (raw
// sockets, SSH, custom TCP) cannot leave the internal network at all.
// `docker://` actions and named runs.image pulls still work: the daemon
// resolves them host-side before the container starts.

import (
	"context"
	"crypto/tls"
	"debug/elf"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// egressProxyPort is the fixed listen port inside the proxy container; the
// job reaches it by network alias, never by host port.
const (
	egressProxyPort = 3128
	egressAlias     = "kern-egress"
)

// defaultEgressAllowlist covers the endpoints CI jobs legitimately need:
// GitHub (git smart-HTTP, API, release/assets/CDN hosts) and the major
// language package registries. Entries are domain suffixes — "github.com"
// covers github.com and every *.github.com subdomain but never
// "notgithub.com". Operators extend or replace the list with
// KERN_EGRESS_ALLOWLIST (comma-separated); KERN_RUNNER_EGRESS=off disables
// the restriction entirely and is documented as an operator escape hatch.
func defaultEgressAllowlist() []string {
	return []string{
		"github.com", "githubusercontent.com", "githubassets.com", "githubapp.com",
		"github.io", "ghcr.io", "githubcopilot.com",
		"npmjs.org", "yarnpkg.com", "pnpm.io",
		"pypi.org", "pythonhosted.org",
		"golang.org", "googlesource.com", "googleapis.com",
		"docker.io", "docker.com", "cloudflare-docker.com",
		"mcr.microsoft.com", "nuget.org", "nuget.org",
		"rubygems.org", "crates.io", "static.crates.io",
		"repo.maven.apache.org", "repo1.maven.org", "plugins.gradle.org", "services.gradle.org",
		"dl.google.com", "maven.google.com",
		"registry.npmjs.org",
	}
}

// egressAllowlist matches hostnames against configured suffixes.
type egressAllowlist struct {
	suffixes []string
}

func newEgressAllowlist(domains []string) *egressAllowlist {
	out := make([]string, 0, len(domains))
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(d, ".")))
		if d != "" {
			out = append(out, d)
		}
	}
	return &egressAllowlist{suffixes: out}
}

func (a *egressAllowlist) allows(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "" {
		return false
	}
	for _, d := range a.suffixes {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

// egressRestricted reports whether this job's containers must sit on the
// allowlisted internal network. Fork trust only — same-repo pull_request
// runs keep full egress (documented in docs/security-isolation.md).
func (e *executor) egressRestricted() bool {
	return e.claim.Run.TrustLevel == "fork" && !e.privileged() && e.cfg.EgressMode != "off"
}

// egressNet names the per-job internal network. It doubles as the service
// network for fork jobs so services and the job share one isolated bridge.
func (e *executor) egressNet() string {
	return "kern-ego-" + sanitizeID(e.claim.Job.ID)
}

func (e *executor) egressProxyName() string {
	return "kern-egro-" + sanitizeID(e.claim.Job.ID)
}

// egressEnv is the proxy environment injected into the job container,
// service containers and docker action containers.
func (e *executor) egressEnv() map[string]string {
	proxy := fmt.Sprintf("http://%s:%d", egressAlias, egressProxyPort)
	env := map[string]string{
		"HTTP_PROXY":  proxy,
		"HTTPS_PROXY": proxy,
		"http_proxy":  proxy,
		"https_proxy": proxy,
		// NO_PROXY keeps service-to-service and loopback traffic direct.
		"NO_PROXY": egressNoProxy(e.claim.Job.Services),
		"no_proxy": egressNoProxy(e.claim.Job.Services),
	}
	// JVMs ignore the *_proxy env vars entirely — Gradle/Maven/Surefire
	// downloads would go direct and die on the internal-only network.
	// Hand the same proxy to java as system properties instead.
	// nonProxyHosts wants '|' separators, not the env var's commas.
	nonProxy := strings.ReplaceAll(env["NO_PROXY"], ",", "|")
	env["JAVA_TOOL_OPTIONS"] = fmt.Sprintf(
		"-Dhttp.proxyHost=%s -Dhttp.proxyPort=%d -Dhttps.proxyHost=%s -Dhttps.proxyPort=%d -Dhttp.nonProxyHosts=%s -Dhttps.nonProxyHosts=%s",
		egressAlias, egressProxyPort, egressAlias, egressProxyPort, nonProxy, nonProxy)
	return env
}

func egressNoProxy(services []ClaimService) string {
	parts := []string{"localhost", "127.0.0.1", "::1"}
	for _, s := range services {
		if s.Name != "" {
			parts = append(parts, s.Name)
		}
	}
	return strings.Join(parts, ",")
}

// applyEgressEnv merges the proxy environment into a container env map.
// Proxy keys always win: workflow env must not redirect around the
// allowlist. Callers add "--network", e.egressNet() to the docker args.
func (e *executor) applyEgressEnv(env map[string]string) {
	if !e.egressRestricted() {
		return
	}
	for k, v := range e.egressEnv() {
		env[k] = v
	}
}

// egressNetArgs returns the docker args attaching a sibling container
// (action or lifecycle hook) to the job's internal network. Empty for
// unrestricted jobs; callers still merge applyEgressEnv for the proxy vars.
func (e *executor) egressNetArgs() []string {
	if !e.egressRestricted() {
		return nil
	}
	return []string{"--network", e.egressNet()}
}

// egressImageMu serializes image staging across concurrent fork jobs —
// two executors must not copy/build the proxy image at once.
var egressImageMu sync.Mutex

// ensureEgress creates the internal network, builds the proxy image if
// needed and starts the per-job proxy container. Runs before the job
// container so --network resolves.
func (e *executor) ensureEgress(ctx context.Context) error {
	if !e.egressRestricted() {
		return nil
	}
	net := e.egressNet()
	if out, err := exec.CommandContext(ctx, "docker", "network", "create", "--internal", net).CombinedOutput(); err != nil {
		if !strings.Contains(string(out), "already exists") {
			return fmt.Errorf("create egress network: %s", tailStr(string(out), 300))
		}
	}
	image, err := e.ensureEgressImage(ctx)
	if err != nil {
		return err
	}
	// A stale proxy from a dead attempt must not squat the name.
	_ = exec.CommandContext(ctx, "docker", "rm", "-f", e.egressProxyName()).Run()
	args := []string{
		"run", "-d", "--rm", "--name", e.egressProxyName(),
		"--network", net, "--network-alias", egressAlias,
		"--memory", "128m", "--cpus", "0.25",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		image, "egress-proxy",
		"--listen", fmt.Sprintf(":%d", egressProxyPort),
		"--allow", strings.Join(e.egressAllowlist(), ","),
	}
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("start egress proxy: %s", tailStr(string(out), 300))
	}
	// The proxy's second leg is the default bridge — that is its only way
	// out. It joins after create so the job net stays internal-only.
	if out, err := exec.CommandContext(ctx, "docker", "network", "connect", "bridge", e.egressProxyName()).CombinedOutput(); err != nil {
		_ = exec.CommandContext(ctx, "docker", "rm", "-f", e.egressProxyName()).Run()
		return fmt.Errorf("connect egress proxy outbound: %s", tailStr(string(out), 300))
	}
	// docker run -d returns before the listener binds; the first step is
	// seconds away but poll briefly so a slow start does not flake jobs.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if exec.CommandContext(ctx, "docker", "inspect", "-f", "{{.State.Running}}", e.egressProxyName()).Run() == nil {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("egress proxy did not start")
		}
		time.Sleep(100 * time.Millisecond)
	}
	e.log.Info("egress allowlist active", zap.String("net", net), zap.Strings("allow", e.egressAllowlist()))
	return nil
}

// egressAllowlist resolves the configured allowlist: KERN_EGRESS_ALLOWLIST
// replaces the defaults entirely when set.
func (e *executor) egressAllowlist() []string {
	if e.cfg.EgressAllowlist != "" {
		return strings.Split(e.cfg.EgressAllowlist, ",")
	}
	return defaultEgressAllowlist()
}

// egressImage builds (once per binary version) a scratch image carrying the
// runner binary in egress-proxy mode. The work root is host-visible at the
// same path, so the build context resolves for containerized and bare-metal
// runners alike.
func (e *executor) ensureEgressImage(ctx context.Context) (string, error) {
	egressImageMu.Lock()
	defer egressImageMu.Unlock()
	const tag = "kern-egress:local"
	dir := filepath.Join(e.cfg.WorkRoot, "_egress")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	dst := filepath.Join(dir, "kern-runner")
	if st, err := os.Stat(dst); err != nil || !sameBinary(st, exe) {
		if err := copyFile(exe, dst); err != nil {
			return "", fmt.Errorf("stage egress proxy binary: %w", err)
		}
		// copyFile stamps a fresh mtime; restore the source's so the
		// image-freshness check below compares like with like.
		if src, err := os.Stat(exe); err == nil {
			_ = os.Chtimes(dst, src.ModTime(), src.ModTime())
		}
	}
	if e.staleEgressImage(ctx, tag, dst) {
		base, err := e.egressBaseImage(ctx, dst)
		if err != nil {
			return "", err
		}
		df := "FROM " + base + "\nCOPY kern-runner /kern-runner\nENTRYPOINT [\"/kern-runner\"]\n"
		if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(df), 0o644); err != nil {
			return "", err
		}
		if out, err := exec.CommandContext(ctx, "docker", "build", "-q", "-t", tag, dir).CombinedOutput(); err != nil {
			return "", fmt.Errorf("build egress proxy image: %s", tailStr(string(out), 300))
		}
	}
	return tag, nil
}

// egressBaseImage picks the image the proxy binary is packaged into.
// CGO_ENABLED=0 runner binaries (every shipped image) are static and run on
// scratch — no pull at all. Dynamically linked dev builds (`go run`,
// CGO_ENABLED=1) need a libc; fall back to a locally present busybox or
// alpine rather than pulling at runtime. Neither present → clear error,
// never a silent skip of the egress policy.
func (e *executor) egressBaseImage(ctx context.Context, binPath string) (string, error) {
	if binaryIsStatic(binPath) {
		return "scratch", nil
	}
	for _, img := range []string{"busybox:latest", "alpine:latest"} {
		if exec.CommandContext(ctx, "docker", "image", "inspect", img).Run() == nil {
			return img, nil
		}
	}
	return "", fmt.Errorf("egress proxy needs a static runner binary (rebuild with CGO_ENABLED=0) or a local busybox/alpine image")
}

// binaryIsStatic reports whether an ELF binary has no PT_INTERP — i.e. it
// does not need a dynamic loader and can run on a scratch image.
func binaryIsStatic(path string) bool {
	f, err := elf.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			return false
		}
	}
	return true
}

// staleEgressImage reports whether the cached image predates the staged
// binary — image inspect alone cannot see COPY-layer freshness cheaply, so
// compare the image creation time against the binary's mtime.
func (e *executor) staleEgressImage(ctx context.Context, tag, binPath string) bool {
	out, err := exec.CommandContext(ctx, "docker", "image", "inspect", "-f", "{{.Created}}", tag).CombinedOutput()
	if err != nil {
		return true
	}
	st, err := os.Stat(binPath)
	if err != nil {
		return true
	}
	created := strings.TrimSpace(string(out))
	t, err := time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return true
	}
	return t.Before(st.ModTime())
}

func sameBinary(st os.FileInfo, path string) bool {
	src, err := os.Stat(path)
	if err != nil {
		return false
	}
	return st.Size() == src.Size() && st.ModTime().Equal(src.ModTime())
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// stopEgress removes the proxy container; the caller removes the network
// after the job and service containers are gone.
func (e *executor) stopEgress(ctx context.Context) {
	if !e.egressRestricted() {
		return
	}
	_ = exec.CommandContext(ctx, "docker", "rm", "-f", e.egressProxyName()).Run()
}

// RunEgressProxy is the `kern-runner egress-proxy` entrypoint: a minimal
// forward proxy that allows only the configured domain suffixes over
// ports 80/443. CONNECT tunnels are restricted to :443 so the proxy cannot
// be turned into a generic TCP relay (e.g. SSH on :22).
func RunEgressProxy(listen string, allow []string) error {
	if len(allow) == 0 {
		allow = defaultEgressAllowlist()
	}
	srv := &http.Server{
		Addr:              listen,
		Handler:           egressHandler(newEgressAllowlist(allow)),
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return srv.ListenAndServe()
}

// egressHandler builds the proxy handler; split out so tests can mount it
// on httptest servers.
func egressHandler(al *egressAllowlist) http.Handler {
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		MaxIdleConns:          32,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			serveConnect(w, r, al)
			return
		}
		serveForward(w, r, al, transport)
	})
}

func proxyDenied(w http.ResponseWriter) {
	http.Error(w, "kern egress: destination not in allowlist", http.StatusForbidden)
}

// serveConnect handles HTTPS tunnelling. Only port 443 is permitted —
// CONNECT to arbitrary ports would make this an open TCP relay.
func serveConnect(w http.ResponseWriter, r *http.Request, al *egressAllowlist) {
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		http.Error(w, "kern egress: malformed CONNECT target", http.StatusBadRequest)
		return
	}
	if port != "443" || !al.allows(host) {
		proxyDenied(w)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "kern egress: hijack unsupported", http.StatusInternalServerError)
		return
	}
	upstream, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 15*time.Second)
	if err != nil {
		http.Error(w, "kern egress: dial failed", http.StatusBadGateway)
		return
	}
	conn, bufs, err := hj.Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	if _, err := conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		conn.Close()
		upstream.Close()
		return
	}
	// The buffered reader may already hold post-CONNECT bytes.
	go func() {
		_, _ = io.Copy(upstream, bufs.Reader)
		upstream.Close()
	}()
	_, _ = io.Copy(conn, upstream)
	conn.Close()
}

// egressPorts are the only ports plain-HTTP forwarding may use; CONNECT is
// hard-restricted to :443 in serveConnect. A var so tests can grant a port.
var egressPorts = map[string]bool{"": true, "80": true, "443": true}

// serveForward handles plain-HTTP proxy requests (absolute-URI form). Only
// allowlisted ports pass; the allowlist is checked against URL.Hostname.
func serveForward(w http.ResponseWriter, r *http.Request, al *egressAllowlist, tr *http.Transport) {
	if r.URL.Scheme != "http" && r.URL.Scheme != "https" {
		http.Error(w, "kern egress: unsupported scheme", http.StatusBadRequest)
		return
	}
	if !egressPorts[r.URL.Port()] {
		proxyDenied(w)
		return
	}
	if !al.allows(r.URL.Hostname()) {
		proxyDenied(w)
		return
	}
	outreq := r.Clone(r.Context())
	outreq.RequestURI = ""
	stripHopHeaders(outreq.Header)
	resp, err := tr.RoundTrip(outreq)
	if err != nil {
		http.Error(w, "kern egress: upstream error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// hopHeaders are removed from forwarded requests and responses per RFC 7230.
var hopHeaders = []string{
	"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
	"Te", "Trailer", "Transfer-Encoding", "Upgrade", "Proxy-Connection",
}

func stripHopHeaders(h http.Header) {
	// Headers nominated by Connection are hop-by-hop too — read the list
	// before deleting the header itself.
	for _, f := range h.Values("Connection") {
		for _, name := range strings.Split(f, ",") {
			h.Del(strings.TrimSpace(name))
		}
	}
	for _, k := range hopHeaders {
		h.Del(k)
	}
}
