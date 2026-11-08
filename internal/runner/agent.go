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
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/dvorinka/kern/internal/version"
)

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
	// MaxLoadPerCPU pauses claiming while loadavg1 exceeds this multiple of
	// the host's CPU count (KERN_RUNNER_LOAD_GATE). Zero disables the gate.
	MaxLoadPerCPU float64
	// ImageMaxAge is how long a pulled job image may sit unused before the
	// GC removes it (KERN_RUNNER_IMAGE_MAX_AGE). Zero disables collection.
	ImageMaxAge time.Duration
	// ActionsGitBase overrides the git host remote actions are fetched from
	// (KERN_ACTIONS_GIT_BASE) — for GHES installs or action mirrors. Empty
	// derives the host from the run's repo URL, else github.com.
	ActionsGitBase string
	// SelfUpdate replaces this container when the control plane offers a
	// signed runner image (KERN_RUNNER_UPDATE=1). The compose runner on the
	// control-plane host leaves it unset; the host updater rebuilds that one.
	SelfUpdate bool
	// Ephemeral marks a JIT/scale-from-zero runner: after EphemeralIdle with
	// no active jobs it asks the control plane to delete its row and exits.
	// The runner token dies with the row, so it cannot rejoin.
	Ephemeral     bool
	EphemeralIdle time.Duration
	// EgressMode controls fork-tier egress: "" or "allowlist" confines fork
	// jobs to a per-job internal network plus a domain-allowlist proxy;
	// "off" disables the restriction (documented operator escape hatch).
	EgressMode string
	// EgressAllowlist replaces the default domain allowlist when set
	// (KERN_EGRESS_ALLOWLIST, comma-separated suffixes).
	EgressAllowlist string
	// Prepull lists images pulled at startup and hourly
	// (KERN_RUNNER_PREPULL, comma-separated) so job pulls hit the local
	// daemon instead of the registry.
	Prepull []string
	// DiskMinFreePct triggers a Docker reclaim sweep when free space on the
	// work-root filesystem drops below this percent
	// (KERN_RUNNER_DISK_MIN_FREE_PCT, default 10). Zero disables pressure
	// pruning; the stale-workspace sweep always runs.
	DiskMinFreePct float64
	// Executor selects the job execution substrate: "docker" (default)
	// runs steps in ephemeral containers; "host" runs them as local
	// processes on the runner host. Host mode has no isolation and the
	// control plane only lets it claim trusted trust tiers. Empty means
	// docker.
	Executor string
	// CacheMaxBytes caps the persistent _cache tree (language caches,
	// _toolcache, _actions). When the tree exceeds it, the oldest files are
	// evicted until it fits (KERN_RUNNER_CACHE_MAX_BYTES, default 8 GiB).
	// Zero disables the bound; eviction never runs while a job is active.
	CacheMaxBytes int64
}

type Agent struct {
	cfg        Config
	log        *zap.Logger
	c          *client
	id         string
	token      string
	sampler    hostSampler
	active     atomic.Int32
	updating   atomic.Bool
	nextUpdate atomic.Int64
	// retire latches once the ephemeral idle timeout fires; heartbeats carry
	// it until the control plane confirms the row is gone. lastActivity is
	// the unixnano of the last job completion (boot time until then). A
	// parked claim long-poll counts as idle — the control plane refuses
	// deregistration while jobs are active and the lease requeues anything
	// claimed in the residual race.
	retire       atomic.Bool
	lastActivity atomic.Int64
	cancel       context.CancelFunc
	// dockerFn stubs the daemon probe in tests; dockerOK caches the result —
	// `docker version` is an exec, and labels() is built per registration.
	dockerFn   func() bool
	dockerOnce sync.Once
	dockerOKV  bool
}

func New(cfg Config, log *zap.Logger) *Agent {
	if cfg.WorkRoot == "" {
		cfg.WorkRoot = os.TempDir() + "/kern-runner"
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 1
	}
	a := &Agent{cfg: cfg, log: log}
	a.lastActivity.Store(time.Now().UnixNano())
	return a
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
		"version":      version.Version,
		"ephemeral":    a.cfg.Ephemeral,
		"executor":     a.executor(),
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

// SetToken skips registration: the agent runs with a previously issued runner
// credential (written by `kern-runner join`).
func (a *Agent) SetToken(token string) {
	a.token = token
	a.c = newClient(a.cfg.ControlURL, token)
}

// Token exposes the credential issued at registration for the join flow.
func (a *Agent) Token() string { return a.token }

// executor is the effective execution substrate; anything but "host" is docker.
func (a *Agent) executor() string {
	if a.cfg.Executor == "host" {
		return "host"
	}
	return "docker"
}

func (a *Agent) labels() []string {
	return a.labelsFor(runtime.GOOS, runtime.GOARCH)
}

// labelsFor computes the advertised label set for a platform. Split from
// labels() so tests can exercise the darwin mapping on a linux host.
func (a *Agent) labelsFor(goos, arch string) []string {
	base := []string{"self-hosted", "kern", goos + "-" + arch}
	switch arch {
	case "amd64":
		base = append(base, "x64")
	case "arm64":
		base = append(base, "arm64")
	}
	// OS-specific labels: linux runners also answer ubuntu-* workflow
	// selectors; darwin runners answer macos. Neither may leak onto the
	// other platform — a darwin runner claiming linux-* jobs was a real
	// scheduling hazard before this fix.
	switch goos {
	case "linux":
		base = append(base, "linux", "linux-"+arch)
		if arch == "amd64" {
			base = append(base, "linux-x64", "ubuntu-latest", "ubuntu-24.04")
		} else if arch == "arm64" {
			base = append(base, "linux-arm64")
		}
	case "darwin":
		base = append(base, "macos")
	case "windows":
		// GitHub's windows-latest tracks the newest client/Server image;
		// versioned labels (windows-2022 etc.) stay opt-in via KERN_RUNNER_LABELS.
		base = append(base, "windows", "windows-latest")
	}
	// The docker label advertises container execution; a host-mode runner
	// must never carry it even if a daemon happens to be installed.
	if a.executor() == "docker" && a.dockerOK() {
		base = append(base, "docker")
	}
	// kern-docker is the inverse capability: a usable docker CLI+daemon
	// inside host-executed jobs (image builds). Host executors only — the
	// docker label already means container execution, not CLI access.
	if a.executor() == "host" && a.dockerOK() {
		base = append(base, "kern-docker")
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
	ctx, a.cancel = context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.heartbeatLoop(ctx)
	}()
	if a.cfg.Ephemeral {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.ephemeralLoop(ctx)
		}()
	}
	if a.executor() == "docker" {
		if a.cfg.ImageMaxAge > 0 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				a.imageGCLoop(ctx)
			}()
		}
		if len(a.cfg.Prepull) > 0 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				a.prepullLoop(ctx)
			}()
		}
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.diskGCLoop(ctx)
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
			sample := a.sampler.sample(a.cfg.WorkRoot, int(a.active.Load()))
			sample.SelfUpdate = a.cfg.SelfUpdate
			sample.Deregister = a.retire.Load()
			resp, err := a.c.heartbeat(hctx, sample)
			cancel()
			if err != nil {
				failures++
				if failures%6 == 1 {
					a.log.Warn("heartbeat failed", zap.Error(err))
				}
				continue
			}
			failures = 0
			if resp.Deregistered {
				a.log.Info("runner deregistered; exiting")
				if a.cancel != nil {
					a.cancel()
				}
				return
			}
			if resp.Update != nil && a.cfg.SelfUpdate && !a.updating.Load() {
				go a.maybeSelfUpdate(ctx, resp.Update.Image, resp.Update.Version)
			}
		}
	}
}

func (a *Agent) maybeSelfUpdate(ctx context.Context, image, ver string) {
	if image == "" || time.Now().Unix() < a.nextUpdate.Load() {
		return
	}
	if !a.updating.CompareAndSwap(false, true) {
		return
	}
	defer a.updating.Store(false)
	a.log.Info("runner self-update", zap.String("version", ver), zap.String("image", image))
	uctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	err := applySelfUpdate(uctx, image, func() bool { return a.active.Load() != 0 })
	if err != nil {
		a.log.Warn("self-update failed", zap.Error(err))
		a.nextUpdate.Store(time.Now().Add(selfUpdateHold).Unix())
	}
}

func (a *Agent) workerLoop(ctx context.Context, n int) {
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if a.updating.Load() {
			sleepCtx(ctx, time.Second)
			continue
		}
		if a.overLoaded() {
			sleepCtx(ctx, 5*time.Second)
			continue
		}
		if a.retire.Load() {
			// Ephemeral runner is draining: stop claiming so the
			// deregistration heartbeat is not racing fresh leases.
			sleepCtx(ctx, 5*time.Second)
			continue
		}
		claim, err := a.c.claim(ctx, 25)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			a.log.Warn("claim failed", zap.Int("worker", n), zap.Error(err), zap.Duration("retry_in", backoff))
			sleepCtx(ctx, backoff)
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		if claim == nil {
			continue
		}
		a.active.Add(1)
		a.executeJob(ctx, claim)
		a.active.Add(-1)
		a.lastActivity.Store(time.Now().UnixNano())
	}
}

// ephemeralLoop latches the retire flag once the runner has been continuously
// idle (no active jobs, no claim in flight) for EphemeralIdle. Heartbeats then
// carry the deregister request until the control plane confirms deletion.
func (a *Agent) ephemeralLoop(ctx context.Context) {
	idle := a.cfg.EphemeralIdle
	if idle <= 0 {
		idle = 2 * time.Minute
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if a.active.Load() != 0 || a.retire.Load() {
			continue
		}
		if time.Since(time.Unix(0, a.lastActivity.Load())) < idle {
			continue
		}
		a.retire.Store(true)
		a.log.Info("ephemeral idle timeout; requesting deregistration", zap.Duration("idle", idle))
	}
}

// overLoaded reports whether the claim gate is engaged: loadavg1 above
// MaxLoadPerCPU per CPU means the host is saturated and new work would only
// slow the jobs already running.
func (a *Agent) overLoaded() bool {
	if a.cfg.MaxLoadPerCPU <= 0 {
		return false
	}
	load, ok := loadAvg1()
	if !ok {
		return false
	}
	if load <= a.cfg.MaxLoadPerCPU*float64(runtime.NumCPU()) {
		return false
	}
	a.log.Warn("load gate: host saturated, pausing claims",
		zap.Float64("loadavg1", load), zap.Float64("gate", a.cfg.MaxLoadPerCPU*float64(runtime.NumCPU())))
	return true
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

	// GitHub's job-setup phase: every declared pre hook runs before the first
	// step. Remote actions download now — the "prepare required actions" pass.
	exec.runPreHooks(ctx)

	jobStatus := "success"
	for _, step := range claim.Steps {
		if ctx.Err() != nil {
			_ = a.c.finishStep(context.Background(), claim.Job.ID, step.Number, "cancelled", 0, nil, nil)
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
		// Registered post hooks pop LIFO first — a failed post fails the job
		// but never stops the remaining hooks.
		if !exec.runPostHooks(ctx) {
			jobStatus = "failure"
		}
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

// dockerOK reports whether a docker daemon answers on this host, probed once.
func (a *Agent) dockerOK() bool {
	a.dockerOnce.Do(func() {
		a.dockerOKV = dockerAvailable()
		if a.dockerFn != nil {
			a.dockerOKV = a.dockerFn()
		}
	})
	return a.dockerOKV
}

func totalMemory() int64 {
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
		if err == nil {
			var b int64
			if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &b); err == nil {
				return b
			}
		}
		return 0
	}
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
