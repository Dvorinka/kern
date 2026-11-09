package runner

// Service containers (jobs.<id>.services). A service without ports runs on a
// dedicated per-job network where its name resolves as a DNS alias — GitHub's
// container-job model (servicename:port). A service that declares ports shares
// the job container's network namespace instead — GitHub's VM model where the
// published port lands on the runner's localhost; the job container also gets
// a /etc/hosts entry so servicename:port keeps working.
// The job container joins the same network in startContainer.

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// serviceNet names the per-job bridge network. Fork-trust jobs already sit
// on the egress-restricted internal network, which doubles as their service
// network so services inherit the same egress policy.
func (e *executor) serviceNet() string {
	if e.egressRestricted() {
		return e.egressNet()
	}
	return "kern-svc-" + sanitizeID(e.claim.Job.ID)
}

// networkedServices returns the services that join the dedicated job network
// (those without ports); port-declaring services share the job netns.
func (e *executor) networkedServices() []ClaimService {
	var out []ClaimService
	for _, svc := range e.claim.Job.Services {
		if len(svc.Ports) == 0 {
			out = append(out, svc)
		}
	}
	return out
}

// ensureServiceNet creates the per-job bridge when any service needs it.
// It runs before the job container so the container can join the net.
func (e *executor) ensureServiceNet(ctx context.Context) error {
	// The egress network is created --internal by ensureEgress; creating it
	// here as a normal bridge would silently lift the egress restriction.
	if e.egressRestricted() || len(e.networkedServices()) == 0 {
		return nil
	}
	out, err := exec.CommandContext(ctx, "docker", "network", "create", e.serviceNet()).CombinedOutput()
	if err != nil && !strings.Contains(string(out), "already exists") {
		return fmt.Errorf("create service network: %s", tailStr(string(out), 300))
	}
	return nil
}

// startServices boots every declared service after the job container exists —
// localhost-mode services attach to its network namespace. Health is then
// awaited where the spec carries health options or the image ships a
// HEALTHCHECK.
func (e *executor) startServices(ctx context.Context) error {
	if len(e.claim.Job.Services) == 0 {
		return nil
	}
	if err := e.acquireServicePorts(ctx); err != nil {
		return err
	}
	// streamCtx bounds the docker logs -f followers and the flush pump;
	// stopServiceLogs cancels it once the containers are gone.
	var streamCtx context.Context
	streamCtx, e.svcLogCancel = context.WithCancel(ctx)
	e.svcLogWG.Add(1)
	go func() {
		defer e.svcLogWG.Done()
		e.serviceLogPump(streamCtx)
	}()
	net := e.serviceNet()
	for _, svc := range e.claim.Job.Services {
		if err := e.pullService(ctx, svc); err != nil {
			return fmt.Errorf("service %s: %w", svc.Name, err)
		}
		if err := e.startService(ctx, net, svc); err != nil {
			return fmt.Errorf("service %s: %w", svc.Name, err)
		}
		e.streamServiceLogs(streamCtx, svc)
	}
	for _, svc := range e.claim.Job.Services {
		if err := e.waitServiceHealthy(ctx, svc); err != nil {
			return fmt.Errorf("service %s: %w", svc.Name, err)
		}
	}
	return nil
}

func (e *executor) pullService(ctx context.Context, svc ClaimService) error {
	if svc.Credentials != nil && svc.Credentials.Username != "" {
		if err := e.registryLogin(ctx, svc.Image, svc.Credentials.Username, svc.Credentials.Password); err != nil {
			return fmt.Errorf("registry login: %w", err)
		}
	}
	if err := e.docker(ctx, "pull", "-q", svc.Image); err != nil {
		return err
	}
	recordImageUse(e.cfg.WorkRoot, svc.Image)
	return nil
}

// registryLogin authenticates docker against the registry embedded in image
// for one pull. The credential lives only in a throwaway --config dir that
// is deleted on return — it never touches the runner user's docker config,
// so a hostile workflow cannot exfiltrate it from a later job's image.
func (e *executor) registryLogin(ctx context.Context, image, user, pass string) error {
	dir, err := os.MkdirTemp("", "kern-regcred-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	args := []string{"--config", dir, "login"}
	if reg := registryHost(image); reg != "" {
		args = append(args, reg)
	}
	args = append(args, "-u", user, "--password-stdin")
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdin = strings.NewReader(pass)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker login: %s", tailStr(string(out), 200))
	}
	return nil
}

// registryHost extracts the registry authority from an image reference:
// the first path component when it carries a '.' or ':' or is "localhost";
// empty means Docker Hub, which docker login reaches with no argument.
func registryHost(image string) string {
	image = strings.TrimSpace(image)
	if i := strings.Index(image, "@"); i >= 0 {
		image = image[:i]
	}
	// Strip the tag: a ':' after the last '/', never the registry port.
	if j := strings.LastIndex(image, "/"); j >= 0 {
		if k := strings.Index(image[j:], ":"); k >= 0 {
			image = image[:j+k]
		}
	} else if k := strings.Index(image, ":"); k >= 0 {
		image = image[:k]
	}
	first, _, _ := strings.Cut(image, "/")
	if first == "localhost" || strings.ContainsAny(first, ".:") {
		return first
	}
	return ""
}

func (e *executor) serviceContainerName(svc ClaimService) string {
	return fmt.Sprintf("kern-svc-%s-%s", sanitizeID(e.claim.Job.ID), svc.Name)
}

func (e *executor) startService(ctx context.Context, net string, svc ClaimService) error {
	args := []string{
		"run", "-d", "--rm",
		"--name", e.serviceContainerName(svc),
	}
	switch {
	case e.privileged() && len(svc.Ports) > 0:
		// Host-net job: publish real port mappings on the host's localhost —
		// the same netns the job sees, so host:container remaps work.
		for _, p := range svc.Ports {
			args = append(args, "-p", "127.0.0.1:"+publishSpec(p))
		}
	case len(svc.Ports) > 0 || e.privileged():
		// Localhost-mode service shares the job netns. On host networking
		// that is the host netns; the job's /etc/hosts entry covers the name.
		args = append(args, "--network", "container:"+e.container)
	default:
		args = append(args, "--network", net, "--network-alias", svc.Name)
	}
	args = append(args, svc.Options...) // compile keeps only --health-* flags
	svcEnv := map[string]string{}
	for k, v := range svc.Env {
		svcEnv[k] = v
	}
	e.applyEgressEnv(svcEnv)
	for k, v := range svcEnv {
		args = append(args, "-e", k+"="+v)
	}
	args = append(args, svc.Image)
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("start: %s", tailStr(string(out), 300))
	}
	return e.startPortForwards(ctx, svc)
}

// publishSpec normalises a workflow `ports:` entry ("5433:5432", "5432",
// "8080:80/tcp", "53:53/udp") into docker's host:container publish form,
// preserving the protocol suffix — dropping it would silently publish TCP
// for a UDP service.
func publishSpec(p string) string {
	proto := ""
	if i := strings.LastIndex(p, "/"); i >= 0 {
		proto, p = p[i:], p[:i]
	}
	if i := strings.Index(p, ":"); i >= 0 {
		return p + proto
	}
	return p + ":" + p + proto
}

// portSpecParts splits a compiled ports: entry into host port, container
// port and protocol. Single-port entries report the same port twice.
func portSpecParts(p string) (host, container, proto string) {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		proto, p = p[i:], p[:i]
	}
	if i := strings.Index(p, ":"); i >= 0 {
		return p[:i], p[i+1:], proto
	}
	return p, p, proto
}

// startPortForwards honours `ports:` host-side remaps on bridge jobs. The
// service shares the job's netns and binds its own port on the shared
// localhost; GitHub's contract is that the workflow's host port answers
// instead, so for every host:container pair that differs a sibling
// forwarder container joins the same netns and splices host→container.
// Privileged jobs publish real -p mappings and never reach this.
func (e *executor) startPortForwards(ctx context.Context, svc ClaimService) error {
	if e.privileged() {
		return nil // real 127.0.0.1 -p publishes already applied
	}
	var remaps [][2]string
	for _, p := range svc.Ports {
		host, cont, proto := portSpecParts(p)
		if host == cont {
			continue
		}
		if proto != "" && proto != "/tcp" {
			e.log.Warn("service port remap skipped (tcp only)",
				zap.String("service", svc.Name), zap.String("port", p))
			continue
		}
		remaps = append(remaps, [2]string{host, cont})
	}
	if len(remaps) == 0 {
		return nil
	}
	image, err := e.ensureEgressImage(ctx)
	if err != nil {
		return fmt.Errorf("port-forward image: %w", err)
	}
	for _, m := range remaps {
		name := fmt.Sprintf("%s-fwd-%s", e.serviceContainerName(svc), m[0])
		out, err := exec.CommandContext(ctx, "docker",
			"run", "-d", "--rm", "--name", name,
			"--network", "container:"+e.container,
			image, "port-forward",
			"--listen", ":"+m[0], "--to", "127.0.0.1:"+m[1]).CombinedOutput()
		if err != nil {
			return fmt.Errorf("port forward %s→%s: %s", m[0], m[1], tailStr(string(out), 300))
		}
		e.svcForwarders = append(e.svcForwarders, name)
	}
	return nil
}

// hostPortLocks serializes host-published service ports across the jobs one
// runner executes concurrently. Trusted jobs publish services on the host
// loopback, and the runner process is the contention domain — one
// kern-runner per docker host — so an in-process lock suffices. A contending
// job waits for the port instead of failing at docker-proxy bind time.
var hostPortLocks = struct {
	sync.Mutex
	sems map[string]chan struct{}
}{sems: map[string]chan struct{}{}}

func hostPortSem(key string) chan struct{} {
	hostPortLocks.Lock()
	defer hostPortLocks.Unlock()
	sem, ok := hostPortLocks.sems[key]
	if !ok {
		sem = make(chan struct{}, 1)
		hostPortLocks.sems[key] = sem
	}
	return sem
}

// publishLockKey reduces a workflow ports: entry to the host-side bind it
// will request — ip:hostPort/proto — the exact socket docker-proxy must own.
// "/tcp" and no suffix share a socket, so they share a key.
func publishLockKey(p string) string {
	spec := publishSpec(p)
	host := spec
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	proto := ""
	if i := strings.LastIndexByte(spec, '/'); i >= 0 {
		proto = spec[i:]
		if proto == "/tcp" {
			proto = ""
		}
	}
	return "127.0.0.1:" + host + proto
}

// acquireServicePorts takes a hold on every host port this job's services
// will publish, before any service container starts. Only privileged jobs
// publish on the host — unprivileged services share the per-job netns and
// cannot collide. Keys are sorted so concurrent multi-port jobs cannot
// deadlock on lock ordering. Privileged services without a ports: entry
// share the host netns directly; their bind port is image-defined and
// outside what the runner can lock — a residual collision GitHub's
// VM-per-job model never sees.
func (e *executor) acquireServicePorts(ctx context.Context) error {
	if !e.privileged() {
		return nil
	}
	var keys []string
	for _, svc := range e.claim.Job.Services {
		for _, p := range svc.Ports {
			keys = append(keys, publishLockKey(p))
		}
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)
	for _, key := range keys {
		sem := hostPortSem(key)
		select {
		case sem <- struct{}{}:
			e.portReleases = append(e.portReleases, func() { <-sem })
			continue
		default:
		}
		e.log.Info("waiting for service port held by another job", zap.String("port", key))
		select {
		case sem <- struct{}{}:
			e.portReleases = append(e.portReleases, func() { <-sem })
		case <-ctx.Done():
			e.releaseServicePorts()
			return ctx.Err()
		}
	}
	return nil
}

// releaseServicePorts frees every port hold; cleanup calls it after the
// service containers are removed.
func (e *executor) releaseServicePorts() {
	for i := len(e.portReleases) - 1; i >= 0; i-- {
		e.portReleases[i]()
	}
	e.portReleases = nil
}

// waitServiceHealthy polls until the container reports healthy, or simply
// running when no healthcheck is defined. Two minutes bounds the wait.
func (e *executor) waitServiceHealthy(ctx context.Context, svc ClaimService) error {
	name := e.serviceContainerName(svc)
	deadline := time.Now().Add(2 * time.Minute)
	for {
		out, err := exec.CommandContext(ctx, "docker", "inspect",
			"--format", "{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}",
			name).CombinedOutput()
		if err != nil {
			return fmt.Errorf("inspect: %s", tailStr(string(out), 200))
		}
		switch strings.TrimSpace(string(out)) {
		case "healthy", "running":
			return nil
		case "unhealthy":
			return fmt.Errorf("healthcheck reports unhealthy")
		case "exited", "dead", "removing":
			logs, _ := exec.CommandContext(ctx, "docker", "logs", "--tail", "20", name).CombinedOutput()
			return fmt.Errorf("service exited during startup: %s", tailStr(string(logs), 300))
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for service to become healthy")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// streamServiceLogs follows `docker logs -f` for a service and folds its
// output into the job's step logs under a service:<name> marker. KERN has no
// job-level log surface, so lines land in whichever step is active — the
// same merged-timeline approximation GitHub's UI makes. Lines arriving
// before the first step or between steps buffer and flush into the next
// step's log; a chatty service is capped so a flood cannot grow memory.
func (e *executor) streamServiceLogs(ctx context.Context, svc ClaimService) {
	name := e.serviceContainerName(svc)
	cmd := exec.CommandContext(ctx, "docker", "logs", "-f", "--tail", "0", name)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return
	}
	if err := cmd.Start(); err != nil {
		e.log.Debug("service log stream unavailable", zap.String("service", svc.Name), zap.Error(err))
		return
	}
	e.svcLogWG.Add(1)
	go func() {
		defer e.svcLogWG.Done()
		var wg sync.WaitGroup
		for _, r := range []io.Reader{stdout, stderr} {
			wg.Add(1)
			go func(r io.Reader) {
				defer wg.Done()
				sc := bufio.NewScanner(r)
				sc.Buffer(make([]byte, 64*1024), 1024*1024)
				for sc.Scan() {
					e.svcLogLine(svc.Name, sc.Text())
				}
			}(r)
		}
		wg.Wait()
		_ = cmd.Wait()
	}()
}

// svcLogPendingCap bounds buffered service output between flushes; overflow
// drops the oldest quarter and marks the gap so logs never look complete.
const svcLogPendingCap = 256 << 10

func (e *executor) svcLogLine(svc, line string) {
	msg := []byte("service:" + svc + " | " + line + "\n")
	e.svcLogMu.Lock()
	if len(e.svcLogPending)+len(msg) > svcLogPendingCap {
		cut := len(e.svcLogPending) - svcLogPendingCap/4
		if cut < 0 {
			cut = 0
		}
		e.svcLogPending = append([]byte("service:log | ... truncated ...\n"), e.svcLogPending[cut:]...)
	}
	e.svcLogPending = append(e.svcLogPending, msg...)
	e.svcLogMu.Unlock()
}

// flushServiceLogs posts buffered service output to the most recently
// started step. No-op before the first step or when nothing is pending.
func (e *executor) flushServiceLogs() {
	e.svcLogMu.Lock()
	data := e.svcLogPending
	e.svcLogPending = nil
	e.svcLogMu.Unlock()
	n := e.currentStep.Load()
	if len(data) == 0 || n <= 0 {
		if len(data) > 0 {
			e.svcLogMu.Lock()
			e.svcLogPending = append(data, e.svcLogPending...)
			e.svcLogMu.Unlock()
		}
		return
	}
	_ = e.c.appendLog(context.Background(), e.jobID, int(n), data)
}

// serviceLogPump flushes pending service lines every 2s so output appears
// during long quiet steps, not only at step boundaries. It also flushes on
// shutdown via stopServiceLogs.
func (e *executor) serviceLogPump(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.flushServiceLogs()
		}
	}
}

func (e *executor) stopServiceLogs(ctx context.Context) {
	if e.svcLogCancel != nil {
		e.svcLogCancel()
	}
	e.svcLogWG.Wait()
	e.flushServiceLogs()
}

// stopServices removes the service containers and the job network; the job
// container leaves the network when it is removed in cleanup. Services
// sharing the job netns die with it — the rm calls below are belt and braces.
func (e *executor) stopServices(ctx context.Context) {
	e.stopServiceLogs(ctx)
	for _, svc := range e.claim.Job.Services {
		_ = exec.CommandContext(ctx, "docker", "rm", "-f", e.serviceContainerName(svc)).Run()
	}
	for _, name := range e.svcForwarders {
		_ = exec.CommandContext(ctx, "docker", "rm", "-f", name).Run()
	}
	e.svcForwarders = nil
	e.stopEgress(ctx)
	if (e.egressRestricted() || len(e.networkedServices()) > 0) && !e.privileged() {
		_ = exec.CommandContext(ctx, "docker", "network", "rm", e.serviceNet()).Run()
	}
	e.releaseServicePorts()
}
