// Package runner is the KERN runner agent. It pulls jobs from the control plane over
// outbound HTTPS, executes each job in an ephemeral container and streams logs back.
// No inbound connectivity to the runner host is required.
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"
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
	// ActionsPreseed lists owner/repo@ref remote actions warmed into the
	// persistent _actions cache at startup and hourly
	// (KERN_ACTIONS_PRESEED, comma-separated). Nil means the built-in
	// fleet set; an explicit list replaces it.
	ActionsPreseed []string
	// DiskMinFreePct triggers a Docker reclaim sweep and pauses job claims
	// when free space on the work-root filesystem drops below this percent
	// (KERN_RUNNER_DISK_MIN_FREE_PCT, default 10). Zero disables both;
	// the stale-workspace sweep always runs.
	DiskMinFreePct float64
	// MemMinFreeBytes pauses job claims while host MemAvailable drops
	// below this floor (KERN_RUNNER_MEM_MIN_FREE_BYTES, default 1 GiB).
	// A job claimed under memory pressure is a job the kernel may
	// OOM-kill mid-step — and on shared hosts it starves neighbours.
	// Zero disables the gate.
	MemMinFreeBytes int64
	// DiskCriticalFreePct and DiskCriticalFloorBytes mark the critical
	// reclaim tier — whichever trips first escalates beyond the docker
	// prune: ledger images unused >24h, workspaces >2h, and the _cache
	// floor halves (KERN_RUNNER_DISK_CRITICAL_FREE_PCT default 5,
	// KERN_RUNNER_DISK_CRITICAL_FLOOR_BYTES default 2 GiB). Zero disables
	// each bound. Idle-gated like the warn tier.
	DiskCriticalFreePct    float64
	DiskCriticalFloorBytes int64
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
	// JobDiskBytes is the runner-wide ceiling on a job's work-root usage
	// (KERN_JOB_DISK, e.g. "20g"). Breaching it aborts the job early with a
	// disk_quota diagnosis instead of letting it die on ENOSPC mid-write.
	// Zero falls back to half the free space on the work-root filesystem
	// at job start (#333).
	JobDiskBytes int64
	// BuildxKeepBytes bounds BuildKit cache retained when disk pressure
	// prunes the builder (KERN_BUILDX_KEEP, e.g. "20g"; default 20 GiB).
	// Pruning to a budget keeps recent build layers warm instead of the
	// old wipe-everything -af (#333).
	BuildxKeepBytes int64
	// StepSilenceSeconds is the no-output watchdog interval
	// (KERN_STEP_SILENCE_TIMEOUT, default 600). After this many seconds
	// without step output the runner dumps process state into the log; at
	// twice the interval it kills the step, classifying the failure as a
	// hang rather than a wall-clock timeout. Zero disables the watchdog.
	StepSilenceSeconds int
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
	// diskPaused latches while the work-root filesystem is under pressure so
	// the transition is logged once per episode, not once per worker tick.
	diskPaused atomic.Bool
	// memPaused latches while host memory is under the claim floor.
	memPaused atomic.Bool
	// loadPaused latches while the load gate is engaged, for the same
	// edge-triggered logging — overLoaded runs per heartbeat now that
	// claimsPaused reports it.
	loadPaused atomic.Bool
	// retire latches once the ephemeral idle timeout fires; heartbeats carry
	// it until the control plane confirms the row is gone. lastActivity is
	// the unixnano of the last job completion (boot time until then). A
	// parked claim long-poll counts as idle — the control plane refuses
	// deregistration while jobs are active and the lease requeues anything
	// claimed in the residual race.
	retire       atomic.Bool
	lastActivity atomic.Int64
	cancel       context.CancelFunc
	// activeJobs holds the IDs of jobs currently executing on this runner —
	// the orphan sweep uses it to tell live kern-svc-*/kern-job-* containers
	// from leaks left by crashed or restarted predecessors.
	activeJobs sync.Map
	// jobContainers maps an active job ID to its docker container name, set
	// once prepare brings the container up (empty for host executors).
	jobContainers sync.Map
	// execSessions dedupes heartbeat-delivered exec offers by request ID.
	execSessions sync.Map
	// dockerFn stubs the daemon probe in tests; dockerOK caches the result —
	// `docker version` is an exec, and labels() is built per registration.
	dockerFn func() bool
	// envOverrides carries operator-managed env from heartbeats, merged
	// into every job's base env (app values win over container defaults).
	envMu        sync.RWMutex
	envOverrides map[string]string
	// toolsetMu guards the org-declared host toolset (#344): toolset is
	// the latest heartbeat-delivered list, toolsetHeal the self_heal=auto
	// mirror, and toolsetCache the last sweep's missing list (toolsetValid
	// once a sweep ran — the report distinguishes "no drift" from "not
	// checked").
	toolsetMu    sync.Mutex
	toolset      []string
	toolsetHeal  bool
	toolsetCache []string
	toolsetValid bool
	// toolsetTicks counts heartbeat beats between drift sweeps; LookPath
	// is cheap but the declared set changes rarely, so a per-beat sweep is
	// waste.
	toolsetTicks int
	dockerOnce   sync.Once
	dockerOKV    bool
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

// noteToolset folds the heartbeat-delivered toolset into agent state; a
// changed declaration sweeps immediately so drift shows up on the next
// beat rather than at the next interval.
func (a *Agent) noteToolset(ctx context.Context, declared []string, heal bool) {
	a.toolsetMu.Lock()
	changed := !slices.Equal(a.toolset, declared)
	a.toolset = append([]string(nil), declared...)
	a.toolsetHeal = heal
	a.toolsetMu.Unlock()
	if changed {
		a.sweepToolset(ctx)
	}
}

// sweepToolset diffs the declared toolset against the host PATH. Docker
// executors skip it — inside the runner container, LookPath sees the
// container's PATH, not the host's, and would report phantom drift. With
// toolsetHeal set (org self_heal=auto) missing tools install in the
// background and the next sweep reports the healed state.
func (a *Agent) sweepToolset(ctx context.Context) {
	a.toolsetMu.Lock()
	declared := append([]string(nil), a.toolset...)
	heal := a.toolsetHeal
	a.toolsetMu.Unlock()
	if a.executor() != "host" || len(declared) == 0 {
		a.toolsetMu.Lock()
		a.toolsetCache, a.toolsetValid = nil, a.executor() == "host" && len(declared) == 0
		a.toolsetMu.Unlock()
		return
	}
	missing := missingTools(declared)
	a.toolsetMu.Lock()
	a.toolsetCache, a.toolsetValid = missing, true
	a.toolsetMu.Unlock()
	if len(missing) > 0 {
		a.log.Warn("host toolset drift", zap.Strings("missing", missing), zap.Bool("heal", heal))
		if heal {
			go a.healToolset(ctx, missing)
		}
	}
}

// healToolset runs the curated install plan for each missing tool, then
// re-sweeps so the report reflects the healed state. Installs can take
// minutes (apt update, tarball fetch), so this runs off the heartbeat
// goroutine; ctx is the agent's run context so shutdown cancels mid-install.
func (a *Agent) healToolset(ctx context.Context, missing []string) {
	for _, name := range missing {
		if err := installHostTool(ctx, name); err != nil {
			a.log.Warn("toolset heal failed", zap.String("tool", name), zap.Error(err))
		} else {
			a.log.Info("toolset healed", zap.String("tool", name))
		}
	}
	a.sweepToolset(ctx)
}

// missingToolsReport hands the heartbeat the last sweep result. nil means
// "not reporting" — the control plane leaves the stored list alone; a
// non-nil empty list actively clears recorded drift.
func (a *Agent) missingToolsReport() *[]string {
	a.toolsetMu.Lock()
	defer a.toolsetMu.Unlock()
	if !a.toolsetValid {
		return nil
	}
	m := append([]string(nil), a.toolsetCache...)
	return &m
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
	// The runner's own name rides as a label so runs can pin a job to a
	// specific host — prime/cache-warming fan-outs and operator drills.
	seen := map[string]bool{}
	out := make([]string, 0, len(base)+len(a.cfg.Labels)+1)
	for _, l := range append(base, a.cfg.Name) {
		l = strings.TrimSpace(l)
		if l == "" || seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	for _, l := range a.cfg.Labels {
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
	if seeds := a.cfg.ActionsPreseed; len(seeds) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.preseedActionsLoop(ctx, seeds)
		}()
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
			sample.ClaimsPaused = a.claimsPaused()
			sample.Labels = a.labels()
			a.toolsetTicks++
			if a.toolsetTicks >= 30 {
				a.toolsetTicks = 0
				a.sweepToolset(ctx)
			}
			sample.MissingTools = a.missingToolsReport()
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
			a.envMu.Lock()
			a.envOverrides = resp.Env
			a.envMu.Unlock()
			a.noteToolset(ctx, resp.Toolset, resp.ToolsetHeal)
			if resp.Deregistered {
				a.log.Info("runner deregistered; exiting")
				if a.cancel != nil {
					a.cancel()
				}
				return
			}
			// The server only offers an update when the runner opted in
			// (env self-update or the operator auto_update toggle), so an
			// offer alone is sufficient license to self-replace.
			if resp.Update != nil && !a.updating.Load() {
				go a.maybeSelfUpdate(ctx, resp.Update)
			}
			// Live-exec offers ride the heartbeat; one session per request,
			// only for jobs this runner is actively executing (#132).
			for _, offer := range resp.Exec {
				if _, dup := a.execSessions.LoadOrStore(offer.ID, struct{}{}); dup {
					continue
				}
				if _, active := a.activeJobs.Load(offer.JobID); !active {
					a.execSessions.Delete(offer.ID)
					continue
				}
				go a.execSession(ctx, offer)
			}
		}
	}
}

func (a *Agent) maybeSelfUpdate(ctx context.Context, offer *updateOffer) {
	if offer.Image == "" || time.Now().Unix() < a.nextUpdate.Load() {
		return
	}
	// Already at the offered version: skip the swap. Guards token-mode
	// runners against a stale runners.version server-side re-offering the
	// same release every hold window.
	if strings.TrimPrefix(offer.Version, "v") == strings.TrimPrefix(version.Version, "v") {
		return
	}
	if !a.updating.CompareAndSwap(false, true) {
		return
	}
	defer a.updating.Store(false)
	a.log.Info("runner self-update", zap.String("version", offer.Version), zap.String("image", offer.Image))
	uctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	busy := func() bool { return a.active.Load() != 0 }
	var err error
	if inContainer() {
		err = applySelfUpdate(uctx, offer.Image, busy)
	} else {
		err = applyBinaryUpdate(uctx, offer.Version, offer.Binaries, busy)
	}
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
		if a.diskClaimPaused() {
			// The work-root filesystem is too full to write job workspaces —
			// claiming now would just fail later in prepare or mid-step.
			sleepCtx(ctx, 30*time.Second)
			continue
		}
		if a.memClaimPaused() {
			// Host memory is at the floor — a claimed job would swap or
			// OOM-kill. Wait for headroom rather than take doomed work.
			sleepCtx(ctx, 15*time.Second)
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

// claimsPaused renders the engaged claim gates as a comma-joined reason for
// the heartbeat, in the same order the worker loop consults them. The
// disk/memory latches are written by the gate probes themselves so the
// heartbeat reports the last decision, not a fresh probe.
func (a *Agent) claimsPaused() string {
	var gates []string
	if a.overLoaded() {
		gates = append(gates, "overload")
	}
	if a.diskPaused.Load() {
		gates = append(gates, "disk")
	}
	if a.memPaused.Load() {
		gates = append(gates, "memory")
	}
	return strings.Join(gates, ",")
}

// diskSpaceFn stubs the filesystem probe in tests.
var diskSpaceFn = diskSpace

// diskClaimResumeMarginPct is the hysteresis band above DiskMinFreePct the
// filesystem must recover to before claims resume. Without it a volume
// hovering at the threshold flaps claims on and off every probe.
const diskClaimResumeMarginPct = 3.0

// diskClaimPaused reports whether claims should pause because the work-root
// filesystem has less than Config.DiskMinFreePct free (#210). The same
// threshold drives the docker reclaim sweep in diskGCLoop — a runner that
// cannot write cannot execute jobs, and every claimed-then-failed job just
// requeues on a fleet that may be equally full.
func (a *Agent) diskClaimPaused() bool {
	free, total := diskSpaceFn(a.cfg.WorkRoot)
	// While latched, demand the higher resume threshold so boundary noise
	// cannot oscillate the gate. DiskMinFreePct <= 0 never latches, so the
	// Load() branch cannot resurrect a disabled check.
	pct := a.cfg.DiskMinFreePct
	if a.diskPaused.Load() {
		pct += diskClaimResumeMarginPct
	}
	if !diskUnderPressure(free, total, pct) {
		if a.diskPaused.CompareAndSwap(true, false) {
			a.log.Info("disk pressure cleared: resuming claims")
		}
		return false
	}
	if a.diskPaused.CompareAndSwap(false, true) {
		a.log.Warn("disk pressure: pausing job claims",
			zap.Int64("free_bytes", free), zap.Int64("total_bytes", total),
			zap.Float64("min_free_pct", a.cfg.DiskMinFreePct))
	}
	return true
}

// memAvailableFn stubs the memory probe in tests.
var memAvailableFn = memAvailable

// memClaimPaused reports whether claims should pause because host
// MemAvailable is under Config.MemMinFreeBytes. The load gate covers CPU
// saturation; this covers the other half of the incident shape — a heavy
// build (Gradle-class) that pushes the host into swap and OOM-kills
// whatever hits its cgroup ceiling first, job or neighbour.
func (a *Agent) memClaimPaused() bool {
	floor := a.cfg.MemMinFreeBytes
	if floor <= 0 {
		return false
	}
	avail, ok := memAvailableFn()
	if !ok || avail >= floor {
		if a.memPaused.CompareAndSwap(true, false) {
			a.log.Info("memory pressure cleared: resuming claims")
		}
		return false
	}
	if a.memPaused.CompareAndSwap(false, true) {
		a.log.Warn("memory pressure: pausing job claims",
			zap.Int64("available_bytes", avail), zap.Int64("floor_bytes", floor))
	}
	return true
}

// jobErrorHint annotates runner-side failure signatures that never reach a
// step — prepare, checkout and image-pull errors land in the job error as
// raw text (#209). ENOSPC gets the live free-space reading so the operator
// can tell real exhaustion from a stale report.
func jobErrorHint(msg, workRoot string) string {
	if !strings.Contains(strings.ToLower(msg), "no space left on device") {
		return msg
	}
	free, total := diskSpaceFn(workRoot)
	return fmt.Sprintf("%s — runner disk full: %d MiB free of %d MiB on the work-root filesystem",
		msg, free/(1<<20), total/(1<<20))
}

// loadAvg1Fn and cpusVisibleFn stub the host probes in tests.
var (
	loadAvg1Fn    = loadAvg1
	cpusVisibleFn = cpusVisible
)

// overLoaded reports whether the claim gate is engaged: loadavg1 above
// MaxLoadPerCPU per CPU means the host is saturated and new work would only
// slow the jobs already running. The CPU count comes from /proc/cpuinfo —
// the same kernel that publishes loadavg — not the container's cpuset.
func (a *Agent) overLoaded() bool {
	if a.cfg.MaxLoadPerCPU <= 0 {
		return false
	}
	load, ok := loadAvg1Fn()
	gate := a.cfg.MaxLoadPerCPU * float64(cpusVisibleFn())
	if !ok || load <= gate {
		if a.loadPaused.CompareAndSwap(true, false) {
			a.log.Info("load cleared: resuming claims")
		}
		return false
	}
	if a.loadPaused.CompareAndSwap(false, true) {
		a.log.Warn("load gate: host saturated, pausing claims",
			zap.Float64("loadavg1", load), zap.Float64("gate", gate))
	}
	return true
}

func (a *Agent) executeJob(ctx context.Context, claim *Claim) {
	log := a.log.With(zap.String("job_id", claim.Job.ID), zap.String("job", claim.Job.Name),
		zap.String("run_id", claim.Run.ID), zap.Int("worker", 0))
	log.Info("job claimed", zap.String("image", claim.Job.Image))
	a.activeJobs.Store(claim.Job.ID, struct{}{})
	defer a.activeJobs.Delete(claim.Job.ID)

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
	var metrics json.RawMessage
	defer func() {
		if r := recover(); r != nil {
			errMsg = fmt.Sprintf("runner panic: %v", r)
			conclusion = "failure"
			log.Error("panic while executing job", zap.Any("panic", r))
		}
		fctx, fcancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := a.c.finishJob(fctx, claim.Job.ID, conclusion, outputs, errMsg, metrics); err != nil {
			log.Error("finish job", zap.Error(err))
		}
		fcancel()
	}()

	result, outs, met, err := a.runSteps(jobCtx, log, claim)
	outputs = outs
	metrics = met
	if err != nil {
		errMsg = jobErrorHint(err.Error(), a.cfg.WorkRoot)
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
func (a *Agent) runSteps(ctx context.Context, log *zap.Logger, claim *Claim) (status string, outs map[string]string, met json.RawMessage, err error) {
	secrets, vars, blocked, err := a.c.jobSecrets(ctx, claim.Job.ID)
	if err != nil {
		return "", nil, nil, fmt.Errorf("fetch secrets: %w", err)
	}
	if blocked {
		log.Info("run is untrusted; secrets withheld")
	}

	exec, err := newJobExecutor(a.cfg, a.c, claim, secrets, vars, log)
	if err != nil {
		return "", nil, nil, err
	}
	exec.sweepOrphans = func(ctx context.Context) { a.sweepOrphanServices(ctx) }
	a.envMu.RLock()
	exec.runnerEnv = a.envOverrides
	a.envMu.RUnlock()
	defer exec.cleanup()
	defer func() {
		if exec.metrics != nil {
			met = exec.metrics.json()
		}
	}()

	if err := exec.prepare(ctx); err != nil {
		return "", nil, nil, err
	}
	if len(claim.Steps) > 0 {
		exec.logEnvAudit(claim.Steps[0])
	}

	// The container is up: publish its name so heartbeat-delivered exec
	// requests can docker exec into it. Host executors store "" — their
	// sessions are refused (no container boundary to enter).
	container := ""
	if !exec.host {
		container = exec.container
	}
	a.jobContainers.Store(claim.Job.ID, container)
	defer a.jobContainers.Delete(claim.Job.ID)

	// GitHub's job-setup phase: every declared pre hook runs before the first
	// step. Remote actions download now — the "prepare required actions" pass.
	exec.runPreHooks(ctx)

	jobStatus := "success"
	for _, step := range claim.Steps {
		if ctx.Err() != nil {
			_ = a.c.finishStep(context.Background(), claim.Job.ID, step.Number, "cancelled", 0, nil, nil)
			return "cancelled", nil, nil, ctx.Err()
		}
		conclusion, stepErr := exec.runStep(ctx, step)
		if stepErr != nil {
			return "", nil, nil, stepErr
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
	return jobStatus, outputs, nil, nil
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

// hostDaemonAlive backs the daemon_gone diagnosis: true while dockerd
// answers, and — critically — true when no docker client exists at all.
// A host without the binary never promised a daemon, so daemon-looking
// errors in a step's tail there are the job's own tooling, not dead infra.
func hostDaemonAlive() bool {
	if _, err := exec.LookPath("docker"); err != nil {
		return true
	}
	return dockerAvailable()
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
