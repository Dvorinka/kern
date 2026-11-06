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
	"strings"
	"time"
)

// serviceNet names the per-job bridge network.
func (e *executor) serviceNet() string {
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
	if len(e.networkedServices()) == 0 {
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
	if len(svc.Ports) > 0 {
		args = append(args, "--network", "container:"+e.container)
	} else {
		args = append(args, "--network", net, "--network-alias", svc.Name)
	}
	args = append(args, svc.Options...) // compile keeps only --health-* flags
	for k, v := range svc.Env {
		args = append(args, "-e", k+"="+v)
	}
	args = append(args, svc.Image)
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("start: %s", tailStr(string(out), 300))
	}
	return nil
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
	if len(e.networkedServices()) > 0 {
		_ = exec.CommandContext(ctx, "docker", "network", "rm", e.serviceNet()).Run()
	}
}
