// Package runner is the KERN runner agent. It pulls jobs from the control plane over
// outbound HTTPS, executes each job in an ephemeral container and streams logs back.
// No inbound connectivity to the runner host is required.
package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

const Version = "0.1.0"

type Config struct {
	ControlURL    string
	JoinToken     string
	Name          string
	Labels        []string
	WorkRoot      string
	Concurrency   int
	JobCPUs       string
	JobMemory     string
	LeaseDuration time.Duration
}

type Agent struct {
	cfg   Config
	log   *zap.Logger
	c     *client
	id    string
	token string
}

func New(cfg Config, log *zap.Logger) *Agent {
	if cfg.WorkRoot == "" {
		cfg.WorkRoot = os.TempDir() + "/kern-runner"
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 1
	}
	return &Agent{cfg: cfg, log: log}
}

// Register authenticates with the join token and receives a per-runner credential.
func (a *Agent) Register(ctx context.Context) error {
	a.c = newClient(a.cfg.ControlURL, "")
	hostname, _ := os.Hostname()
	name := a.cfg.Name
	if name == "" {
		name = hostname
	}
	req := map[string]any{
		"join_token":   a.cfg.JoinToken,
		"name":         name,
		"hostname":     hostname,
		"os":           runtime.GOOS,
		"arch":         runtime.GOARCH,
		"cpus":         runtime.NumCPU(),
		"memory_bytes": totalMemory(),
		"disk_bytes":   totalDisk(a.cfg.WorkRoot),
		"labels":       a.labels(),
		"version":      Version,
	}
	runnerID, token, err := a.c.register(ctx, req)
	if err != nil {
		return fmt.Errorf("register runner: %w", err)
	}
	a.id, a.token = runnerID, token
	a.c = newClient(a.cfg.ControlURL, token)
	a.log.Info("registered with control plane", zap.String("runner_id", runnerID), zap.String("name", name))
	return nil
}

func (a *Agent) labels() []string {
	base := []string{"self-hosted", "kern", runtime.GOOS, "linux-" + string(runtime.GOARCH)}
	switch runtime.GOARCH {
	case "amd64":
		base = append(base, "x64", "linux-x64")
	case "arm64":
		base = append(base, "arm64", "linux-arm64")
	}
	if dockerAvailable() {
		base = append(base, "docker")
	}
	if runtime.GOOS == "linux" {
		base = append(base, "ubuntu-latest", "ubuntu-24.04", "linux")
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(base)+len(a.cfg.Labels))
	for _, l := range append(base, a.cfg.Labels...) {
		l = strings.TrimSpace(l)
		if l == "" || seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	return out
}

// Run starts the heartbeat and worker loops and blocks until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	if a.token == "" {
		if err := a.Register(ctx); err != nil {
			return err
		}
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.heartbeatLoop(ctx)
	}()

	workers := a.cfg.Concurrency
	a.log.Info("runner ready", zap.Int("concurrency", workers), zap.Strings("labels", a.labels()))
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			a.workerLoop(ctx, n)
		}(i)
	}
	wg.Wait()
	return nil
}

func (a *Agent) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := a.c.heartbeat(hctx)
			cancel()
			if err != nil {
				failures++
				if failures%6 == 1 {
					a.log.Warn("heartbeat failed", zap.Error(err))
				}
				continue
			}
			failures = 0
		}
	}
}

func (a *Agent) workerLoop(ctx context.Context, n int) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		claim, err := a.c.claim(ctx, 25)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			a.log.Warn("claim failed", zap.Int("worker", n), zap.Error(err))
			sleepCtx(ctx, 2*time.Second)
			continue
		}
		if claim == nil {
			continue
		}
		a.executeJob(ctx, claim)
	}
}

func (a *Agent) executeJob(ctx context.Context, claim *Claim) {
	log := a.log.With(zap.String("job_id", claim.Job.ID), zap.String("job", claim.Job.Name),
		zap.String("run_id", claim.Run.ID), zap.Int("worker", 0))
	log.Info("job claimed", zap.String("image", claim.Job.Image))

	jobCtx := ctx
	var cancel context.CancelFunc
	if claim.Job.TimeoutSeconds > 0 {
		jobCtx, cancel = context.WithTimeout(ctx, time.Duration(claim.Job.TimeoutSeconds)*time.Second)
	} else {
		jobCtx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	conclusion := "failure"
	errMsg := ""
	var outputs map[string]string
	defer func() {
		if r := recover(); r != nil {
			errMsg = fmt.Sprintf("runner panic: %v", r)
			conclusion = "failure"
			log.Error("panic while executing job", zap.Any("panic", r))
		}
		fctx, fcancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := a.c.finishJob(fctx, claim.Job.ID, conclusion, outputs, errMsg); err != nil {
			log.Error("finish job", zap.Error(err))
		}
		fcancel()
	}()

	result, outs, err := a.runSteps(jobCtx, log, claim)
	outputs = outs
	if err != nil {
		errMsg = err.Error()
		if jobCtx.Err() == context.DeadlineExceeded {
			errMsg = "job timed out"
		}
		if jobCtx.Err() == context.Canceled && !strings.Contains(errMsg, "cancelled") {
			errMsg = "job cancelled"
		}
		log.Warn("job failed", zap.String("error", errMsg))
		if strings.Contains(errMsg, "cancelled") || jobCtx.Err() == context.Canceled {
			conclusion = "cancelled"
		}
		return
	}
	conclusion = result
}

// runSteps executes the compiled step list and returns the conclusion plus job outputs.
func (a *Agent) runSteps(ctx context.Context, log *zap.Logger, claim *Claim) (string, map[string]string, error) {
	secrets, blocked, err := a.c.jobSecrets(ctx, claim.Job.ID)
	if err != nil {
		return "", nil, fmt.Errorf("fetch secrets: %w", err)
	}
	if blocked {
		log.Info("run is untrusted; secrets withheld")
	}

	exec, err := newJobExecutor(a.cfg, a.c, claim, secrets, log)
	if err != nil {
		return "", nil, err
	}
	defer exec.cleanup()

	if err := exec.prepare(ctx); err != nil {
		return "", nil, err
	}

	jobStatus := "success"
	for _, step := range claim.Steps {
		if ctx.Err() != nil {
			_ = a.c.finishStep(context.Background(), claim.Job.ID, step.Number, "cancelled", 0)
			return "cancelled", nil, ctx.Err()
		}
		conclusion, stepErr := exec.runStep(ctx, step)
		if stepErr != nil {
			return "", nil, stepErr
		}
		switch conclusion {
		case "failure":
			jobStatus = "failure"
		case "cancelled":
			jobStatus = "cancelled"
		}
		// On failure, keep walking the list: remaining steps go through
		// stepCondition, which records them as skipped unless their `if:`
		// (e.g. always(), failure()) lets them run. Cancellation hard-stops.
		if conclusion == "cancelled" {
			break
		}
	}

	// Deferred actions/cache saves run at job end (GitHub's post-job phase) so
	// every step has contributed to the workspace. Cancellation skips them.
	if jobStatus != "cancelled" {
		exec.saveCaches()
	}

	var outputs map[string]string
	if len(claim.Job.OutputsSpec) > 0 {
		outputs = exec.evaluateOutputs(jobStatus)
	}
	return jobStatus, outputs, nil
}

func sleepCtx(ctx context.Context, d time.Duration) {

	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func dockerAvailable() bool {
	return exec.Command("docker", "version", "--format", "{{.Server.Version}}").Run() == nil
}

func totalMemory() int64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				var kb int64
				fmt.Sscanf(fields[1], "%d", &kb)
				return kb * 1024
			}
		}
	}
	return 0
}
