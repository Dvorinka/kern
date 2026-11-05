package runner

// executor runs a claimed job in a single ephemeral Docker container. Steps execute
// via `docker exec` so each step exposes its exit code; the container rootfs stays
// read-only and capabilities are dropped (plan.md execution invariants).
//
// State that must survive between steps lives on the mounted host dir /github/cmd:
// GITHUB_ENV accumulates exported variables, GITHUB_PATH carries PATH entries and
// GITHUB_OUTPUT carries step outputs; the host merges the files between steps.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

const (
	containerWorkspace = "/github/workspace"
	containerCmdDir    = "/github/cmd"
	containerScriptDir = "/kern/scripts"
	containerCacheDir  = "/kern/cache"
	jobUID             = 1000 // container process runs unprivileged
)

// cacheDir is the runner-level persistent cache root on the host: it lives
// beside the per-job dirs under WorkRoot but is never removed by cleanup().
func cacheDir(workRoot string) string {
	return filepath.Join(workRoot, "_cache")
}

var (
	imageRe     = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/:@-]*$`)
	nonIDRe     = regexp.MustCompile(`[^a-zA-Z0-9_-]`)
	stepTimeout = 1 * time.Hour
)

type executor struct {
	cfg     Config
	c       *client
	claim   *Claim
	secrets map[string]string
	log     *zap.Logger

	jobID     string
	container string
	// shell is the in-container interpreter; bash when present, sh otherwise
	// (GitHub runners fall back the same way — e.g. alpine images).
	shell string
	enc   *envBuilder

	cancelled    atomic.Bool
	stepResults  map[string]*stepOutcome
	pendingCache []pendingCacheSave

	mu       sync.Mutex
	stop     chan struct{}
	stopped  sync.WaitGroup
	killFn   context.CancelFunc // kills the docker exec of the step in flight
	lastTail string             // log tail of the most recent step, for diagnosis

	scriptSeq  int               // unique script filenames across composite sub-steps
	sumBuf     bytes.Buffer      // composite sub-step summaries, folded into the parent step
	actionDirs map[string]string // remote actions fetched this job, keyed by owner__repo__ref
	exprCtx    map[string]any    // composite sub-step interpolation context; nil = runnerContext()
}

type stepOutcome struct {
	Conclusion string // success | failure | skipped | cancelled
	Outcome    string // conclusion before continue-on-error is applied
	Outputs    map[string]string
}

func newJobExecutor(cfg Config, c *client, claim *Claim, secrets map[string]string, log *zap.Logger) (*executor, error) {
	if !imageRe.MatchString(claim.Job.Image) {
		return nil, fmt.Errorf("invalid container image %q", claim.Job.Image)
	}
	id := sanitizeID(claim.Job.ID)
	if id == "" {
		return nil, fmt.Errorf("job id is empty")
	}
	workRoot := filepath.Join(cfg.WorkRoot, id)
	for _, dir := range []string{"workspace", "cmd", "scripts", "actions"} {
		p := filepath.Join(workRoot, dir)
		if err := os.MkdirAll(p, 0o755); err != nil {
			return nil, fmt.Errorf("create %s dir: %w", dir, err)
		}
		if dir == "scripts" || dir == "actions" {
			continue // mounted read-only
		}
		// Job containers run as uid 1000; MkdirAll honours umask, so chmod
		// explicitly. Docker Desktop's uid mapping hid this on dev machines.
		if err := os.Chmod(p, 0o777); err != nil {
			return nil, fmt.Errorf("chmod %s dir: %w", dir, err)
		}
	}
	// The persistent runner cache survives cleanup(); tool caches pointed at
	// it (GOMODCACHE, npm, pip) make warm runs skip network fetches entirely.
	if err := os.MkdirAll(cacheDir(cfg.WorkRoot), 0o777); err != nil {
		return nil, fmt.Errorf("create runner cache dir: %w", err)
	}
	_ = os.Chmod(cacheDir(cfg.WorkRoot), 0o777) // umask may have narrowed MkdirAll
	return &executor{
		cfg: cfg, c: c, claim: claim, secrets: secrets, log: log,
		jobID:       claim.Job.ID,
		container:   fmt.Sprintf("kern-job-%s-%d", id, claim.Job.Attempt),
		enc:         newEnvBuilder(claim, secrets),
		stepResults: map[string]*stepOutcome{},
		stop:        make(chan struct{}),
	}, nil
}

func sanitizeID(id string) string { return nonIDRe.ReplaceAllString(id, "-") }

func (e *executor) workRoot() string {
	return filepath.Join(e.cfg.WorkRoot, sanitizeID(e.claim.Job.ID))
}

// prepare provisions the workspace and starts the idle job container. Lease
// renewal and the cancel watcher run throughout: a cold image pull can outlast
// the lease, and a requeue would race a second executor for the same job.
func (e *executor) prepare(ctx context.Context) error {
	e.stopped.Add(2)
	go func() {
		defer e.stopped.Done()
		e.leaseHeartbeat()
	}()
	go func() {
		defer e.stopped.Done()
		e.watchCancel()
	}()
	if err := e.checkout(ctx); err != nil {
		e.goStop()
		return fmt.Errorf("checkout: %w", err)
	}
	if err := e.pull(ctx); err != nil {
		e.goStop()
		return fmt.Errorf("pull image: %w", err)
	}
	if err := e.startContainer(ctx); err != nil {
		e.goStop()
		return fmt.Errorf("start container: %w", err)
	}
	return nil
}

func (e *executor) goStop() {
	select {
	case <-e.stop:
	default:
		close(e.stop)
	}
}

// docker runs a docker CLI command, attaching its output to errors.
func (e *executor) docker(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}

func (e *executor) pull(ctx context.Context) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker binary not found on runner host")
	}
	if err := e.docker(ctx, "pull", "-q", e.claim.Job.Image); err != nil {
		return err
	}
	recordImageUse(e.cfg.WorkRoot, e.claim.Job.Image)
	return nil
}

// startContainer creates and starts the idle job container.
func (e *executor) startContainer(ctx context.Context) error {
	workRoot := e.workRoot()
	cmdHome := filepath.Join(workRoot, "cmd")
	scriptDir := filepath.Join(workRoot, "scripts")
	args := []string{
		"create", "--rm", "--name", e.container,
		"--user", fmt.Sprintf("%d:%d", jobUID, jobUID),
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--read-only",
		"--tmpfs", "/tmp:rw,noexec,nosuid,size=512m",
		"--tmpfs", "/run:rw,noexec,nosuid,size=64m",
		"--memory", valueOr(e.cfg.JobMemory, "1g"),
		"--cpus", valueOr(e.cfg.JobCPUs, "1"),
		"-v", filepath.Join(workRoot, "workspace") + ":" + containerWorkspace,
		"-v", cmdHome + ":" + containerCmdDir,
		"-v", scriptDir + ":" + containerScriptDir + ":ro",
		"-v", filepath.Join(workRoot, "actions") + ":" + containerActionsDir + ":ro",
		"-v", cacheDir(e.cfg.WorkRoot) + ":" + containerCacheDir,
		"-e", "KERN_CACHE_DIR=" + containerCacheDir,
		"-e", "GITHUB_WORKSPACE=" + containerWorkspace,
		"-e", "GITHUB_REPOSITORY=" + defaultStr(e.claim.Run.RepoFullName, e.claim.Run.Project),
		"-e", "GITHUB_SHA=" + e.claim.Run.SHA,
		"-e", "GITHUB_REF=" + e.claim.Run.Ref,
		"-e", "GITHUB_RUN_ID=" + e.claim.Run.ID,
		"-e", "GITHUB_RUN_NUMBER=" + itoa(e.claim.Run.Number),
		"-e", "GITHUB_JOB=" + e.claim.Job.Key,
		"-e", "KERN=true", "-e", "CI=true",
		// The workspace is cloned by the runner (uid 0) but steps run as
		// jobUID — tell git the tree is safe so VCS stamping works.
		"-e", "GIT_CONFIG_COUNT=1",
		"-e", "GIT_CONFIG_KEY_0=safe.directory",
		"-e", "GIT_CONFIG_VALUE_0=*",
		"--entrypoint", "/bin/sh",
		e.claim.Job.Image, "-c", "while true; do sleep 30; done",
	}
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker create: %s", strings.TrimSpace(string(out)))
	}
	if err := e.docker(ctx, "start", e.container); err != nil {
		return err
	}
	e.shell = "/bin/bash"
	if exec.CommandContext(ctx, "docker", "exec", e.container,
		"sh", "-c", "command -v bash >/dev/null").Run() != nil {
		e.shell = "/bin/sh"
	}
	return nil
}

// checkout materialises the repository inside the workspace directory. An
// uploaded bundle (kern exec) replaces the git clone entirely.
func (e *executor) checkout(ctx context.Context) error {
	ws := filepath.Join(e.workRoot(), "workspace")
	run := e.claim.Run
	if run.BundleRef != "" {
		bundle := filepath.Join(e.workRoot(), "source.tar.gz")
		if err := e.c.downloadSource(ctx, e.jobID, bundle); err != nil {
			return fmt.Errorf("download source bundle: %w", err)
		}
		return untarGz(bundle, ws)
	}
	if run.LocalPath != "" {
		// Local runs copy the host tree; the source repo stays untouched.
		if out, err := exec.CommandContext(ctx, "git", "clone", "--quiet", "--", run.LocalPath, ws).CombinedOutput(); err != nil {
			return fmt.Errorf("clone %s: %w (%s)", run.LocalPath, err, tailStr(string(out), 400))
		}
		return nil
	}
	if run.RepoURL == "" {
		return fmt.Errorf("run has neither repo_url nor local_path; there is nothing to check out")
	}
	var args []string
	if run.SHA != "" {
		// A SHA checkout needs history, so no --depth.
		args = []string{"clone", "--quiet", "--", run.RepoURL, ws}
	} else {
		args = []string{"clone", "--quiet", "--depth", "1", "--", run.RepoURL, ws}
	}
	if out, err := exec.CommandContext(ctx, "git", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("clone %s: %w (%s)", run.RepoURL, err, tailStr(string(out), 400))
	}
	git := func(gargs ...string) error {
		x := exec.CommandContext(ctx, "git", append([]string{"-C", ws}, gargs...)...)
		out, err := x.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s: %w (%s)", strings.Join(gargs, " "), err, tailStr(string(out), 400))
		}
		return nil
	}
	if run.SHA != "" {
		return git("checkout", "--force", run.SHA)
	}
	if branch := strings.TrimPrefix(run.Ref, "refs/heads/"); branch != "" && branch != run.Ref {
		// A refless clone lands on HEAD; align it with the run branch best effort.
		_ = exec.CommandContext(ctx, "git", "-C", ws, "checkout", "--force", branch).Run()
	}
	return nil
}

// leaseHeartbeat renews the job lease while the job runs.
func (e *executor) leaseHeartbeat() {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-e.stop:
			return
		case <-t.C:
			hctx, hcancel := context.WithTimeout(context.Background(), 10*time.Second)
			cancelled, err := e.c.renew(hctx, e.jobID)
			hcancel()
			if err != nil {
				e.log.Warn("lease renewal failed", zap.String("job_id", e.jobID), zap.Error(err))
				continue
			}
			if cancelled {
				e.markCancelled()
				return
			}
		}
	}
}

// watchCancel polls the control plane for a run-level cancel request.
func (e *executor) watchCancel() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-e.stop:
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			cancelled, err := e.c.cancelCheck(ctx, e.jobID)
			cancel()
			if err == nil && cancelled {
				e.markCancelled()
				return
			}
		}
	}
}

func (e *executor) markCancelled() {
	e.cancelled.Store(true)
	e.kill()
}

// kill aborts the step currently in flight by cancelling its exec command.
// Cancelling an already-finished step context is harmless.
func (e *executor) kill() {
	e.mu.Lock()
	fn := e.killFn
	e.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// cleanup removes the container, the work dir and stops background loops.
func (e *executor) cleanup() {
	e.goStop()
	e.stopped.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "docker", "rm", "-f", e.container).Run()
	_ = os.RemoveAll(e.workRoot())
}

func valueOr(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func defaultStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

func tailStr(s string, n int) string {
	if len(s) <= n {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(s[len(s)-n:])
}
