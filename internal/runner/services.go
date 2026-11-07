package runner

// Service containers (jobs.<id>.services). A service without ports runs on a
// dedicated per-job network where its name resolves as a DNS alias — GitHub's
// container-job model (servicename:port). A service that declares ports shares
// the job container's network namespace instead — GitHub's VM model where the
// published port lands on the runner's localhost; the job container also gets
// a /etc/hosts entry so servicename:port keeps working.
// The job container joins the same network in startContainer.

import (
	"context"
	"fmt"
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
	net := e.serviceNet()
	for _, svc := range e.claim.Job.Services {
		if err := e.pullService(ctx, svc.Image); err != nil {
			return fmt.Errorf("service %s: %w", svc.Name, err)
		}
		if err := e.startService(ctx, net, svc); err != nil {
			return fmt.Errorf("service %s: %w", svc.Name, err)
		}
	}
	for _, svc := range e.claim.Job.Services {
		if err := e.waitServiceHealthy(ctx, svc); err != nil {
			return fmt.Errorf("service %s: %w", svc.Name, err)
		}
	}
	return nil
}

func (e *executor) pullService(ctx context.Context, image string) error {
	if err := e.docker(ctx, "pull", "-q", image); err != nil {
		return err
	}
	recordImageUse(e.cfg.WorkRoot, image)
	return nil
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
	return nil
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

// stopServices removes the service containers and the job network; the job
// container leaves the network when it is removed in cleanup. Services
// sharing the job netns die with it — the rm calls below are belt and braces.
func (e *executor) stopServices(ctx context.Context) {
	for _, svc := range e.claim.Job.Services {
		_ = exec.CommandContext(ctx, "docker", "rm", "-f", e.serviceContainerName(svc)).Run()
	}
	e.stopEgress(ctx)
	if (e.egressRestricted() || len(e.networkedServices()) > 0) && !e.privileged() {
		_ = exec.CommandContext(ctx, "docker", "network", "rm", e.serviceNet()).Run()
	}
	e.releaseServicePorts()
}
