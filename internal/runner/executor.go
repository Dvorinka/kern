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
	"encoding/base64"
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
	containerTmpDir    = "/kern/tmp"
	jobUID             = 1000 // container process runs unprivileged
)

// cacheDir is the runner-level persistent cache root on the host: it lives
// beside the per-job dirs under WorkRoot but is never removed by cleanup().
func cacheDir(workRoot string) string {
	return filepath.Join(workRoot, "_cache")
}

// hostCABundle is the trust store the job container does not ship.
// ubuntu:24.04 has no CA bundle, and Go ignores its embedded roots when
// the system store is missing. The daemon mounts this host path.
const hostCABundle = "/etc/ssl/certs/ca-certificates.crt"

func jobCAMount() []string {
	if _, err := os.Stat(hostCABundle); err != nil {
		return nil
	}
	return []string{"-v", hostCABundle + ":" + hostCABundle + ":ro"}
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
	// stepEnvExtra holds the multi-line vars docker's env-file cannot carry;
	// set per step by writeStepEnv and passed as -e args.
	stepEnvExtra map[string]string

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

	postHooks []*actionHook          // post stack: armed lazily, popped LIFO
	postByKey map[string]*actionHook // dedup between pre-time and main-time arms
	preDone   map[string]bool        // top-level uses-steps whose pre phase was decided
	preFailed map[int]string         // step number -> setup/pre failure; main never runs
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
	for _, dir := range []string{"workspace", "cmd", "scripts", "actions", "home", "tmp"} {
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
	// File-command targets exist before any step runs so non-shell executors
	// (JavaScript and Docker actions) find them already present; containers
	// run as uid 1000 so the files must be world-writable.
	for _, f := range []string{"GITHUB_ENV", "PATH_LINES", "GITHUB_OUTPUT", "STEP_SUMMARY", "GITHUB_STATE"} {
		p := filepath.Join(workRoot, "cmd", f)
		_ = os.WriteFile(p, nil, 0o666)
		_ = os.Chmod(p, 0o666)
	}
	// The persistent runner cache survives cleanup(); tool caches pointed at
	// it (GOMODCACHE, npm, pip) make warm runs skip network fetches entirely.
	if err := os.MkdirAll(cacheDir(cfg.WorkRoot), 0o777); err != nil {
		return nil, fmt.Errorf("create runner cache dir: %w", err)
	}
	_ = os.Chmod(cacheDir(cfg.WorkRoot), 0o777) // umask may have narrowed MkdirAll
	// _node holds the JavaScript action runtime; it must exist before
	// docker create or the daemon plants a root-owned dir the runner
	// cannot provision into.
	if err := os.MkdirAll(filepath.Join(cfg.WorkRoot, "_node"), 0o755); err != nil {
		return nil, fmt.Errorf("create node runtime dir: %w", err)
	}
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
	// Steps run as jobUID but checkout ran as the runner user; hand the
	// workspace over so steps can write inside it (node_modules, dist).
	chownTree(filepath.Join(e.workRoot(), "workspace"), jobUID)
	if err := e.pull(ctx); err != nil {
		e.goStop()
		return fmt.Errorf("pull image: %w", err)
	}
	if err := e.ensureServiceNet(ctx); err != nil {
		e.goStop()
		return fmt.Errorf("service network: %w", err)
	}
	if err := e.startContainer(ctx); err != nil {
		e.goStop()
		return fmt.Errorf("start container: %w", err)
	}
	if err := e.startServices(ctx); err != nil {
		e.goStop()
		return fmt.Errorf("start services: %w", err)
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
	}
	if len(e.networkedServices()) > 0 {
		args = append(args, "--network", e.serviceNet())
	}
	for _, svc := range e.claim.Job.Services {
		if len(svc.Ports) > 0 {
			// Localhost-mode service shares this container's netns; keep the
			// service name resolvable so both addressing styles work.
			args = append(args, "--add-host", svc.Name+":127.0.0.1")
		}
	}
	if !e.privileged() {
		args = append(args,
			"--user", fmt.Sprintf("%d:%d", jobUID, jobUID),
			"--cap-drop", "ALL",
			"--security-opt", "no-new-privileges",
			"--read-only",
			"--tmpfs", "/tmp:rw,noexec,nosuid,size=512m",
			"--tmpfs", "/run:rw,noexec,nosuid,size=64m",
		)
	} else {
		args = append(args, "-v", "/var/run/docker.sock:/var/run/docker.sock")
		// Job images ship no docker client; GitHub's ubuntu image does. The
		// runner seeds its own CLI + plugins into the host-visible work root
		// (docker -v sources resolve on the host), then mounts them read-only.
		cliDir := filepath.Join(e.cfg.WorkRoot, "_cli")
		if seeded, serr := seedDockerCLI(cliDir); serr == nil {
			args = append(args,
				"-v", filepath.Join(seeded, "docker")+":/usr/bin/docker:ro",
			)
			if plugins := filepath.Join(seeded, "cli-plugins"); dirExists(plugins) {
				args = append(args, "-v", plugins+":/usr/libexec/docker/cli-plugins:ro")
			}
		} else {
			e.log.Warn("docker cli seed failed; docker steps will lack the client", zap.Error(serr))
		}
	}
	args = append(args,
		"--memory", valueOr(e.cfg.JobMemory, "1g"),
		"--cpus", valueOr(e.cfg.JobCPUs, "1"),
		"-v", filepath.Join(workRoot, "workspace")+":"+containerWorkspace,
		"-v", cmdHome+":"+containerCmdDir,
		"-v", scriptDir+":"+containerScriptDir+":ro",
		"-v", filepath.Join(workRoot, "actions")+":"+containerActionsDir+":ro",
		"-v", filepath.Join(e.cfg.WorkRoot, "_node")+":"+containerNodeRoot+":ro",
		"-v", cacheDir(e.cfg.WorkRoot)+":"+containerCacheDir,
		"-v", filepath.Join(workRoot, "home")+":"+containerActionHome,
	)
	args = append(args, jobCAMount()...)
	args = append(args,
		"-e", "KERN_CACHE_DIR="+containerCacheDir,
		// setup-* actions resolve @actions/tool-cache via RUNNER_TOOL_CACHE;
		// pointing it at the persistent runner cache makes installed SDKs warm.
		"-e", "RUNNER_TOOL_CACHE="+containerCacheDir+"/_toolcache",
		// RUNNER_TEMP must be exec-allowed: setup-* actions extract and run
		// installers there, and the container's /tmp tmpfs is noexec.
		"-e", "RUNNER_TEMP="+containerTmpDir,
		// go build/test scratch (GOTMPDIR) and generic temp (TMPDIR) must be
		// exec-allowed and roomier than the noexec /tmp tmpfs — `go test`
		// writes and executes binaries there.
		"-e", "GOTMPDIR="+containerTmpDir,
		"-e", "TMPDIR="+containerTmpDir,
		"-v", filepath.Join(workRoot, "tmp")+":"+containerTmpDir,
		// Actions resolve $HOME for tool state (setup-go GOPATH, npm config);
		// images may carry no writable home for uid 1000 at all.
		"-e", "HOME="+containerActionHome,
		// Leave PATH to the image. golang:1.26 puts go on its own PATH, and
		// replacing that hides the toolchain. Node is prepended per step.
		"-e", "GITHUB_WORKSPACE="+containerWorkspace,
		"-e", "GITHUB_REPOSITORY="+defaultStr(e.claim.Run.RepoFullName, e.claim.Run.Project),
		"-e", "GITHUB_SHA="+e.claim.Run.SHA,
		"-e", "GITHUB_REF="+e.claim.Run.Ref,
		"-e", "GITHUB_RUN_ID="+e.claim.Run.ID,
		"-e", "GITHUB_RUN_NUMBER="+itoa(e.claim.Run.Number),
		"-e", "GITHUB_JOB="+e.claim.Job.Key,
		// Actions test GITHUB_ACTIONS to detect CI; gh and release actions
		// read the event/workflow/actor triple.
		"-e", "GITHUB_ACTIONS=true",
		"-e", "GITHUB_EVENT_NAME="+e.claim.Run.Event,
		"-e", "GITHUB_WORKFLOW="+e.claim.Run.Workflow,
		"-e", "GITHUB_ACTOR="+e.claim.Run.Actor,
		"-e", "GITHUB_SERVER_URL="+serverURL(e.claim.Run.RepoURL),
		"-e", "GITHUB_API_URL="+apiURL(e.claim.Run.RepoURL),
		"-e", "RUNNER_OS=Linux",
		"-e", "RUNNER_ARCH=X64",
		"-e", "RUNNER_NAME="+e.cfg.Name,
		"-e", "KERN=true", "-e", "CI=true",
		// The workspace is cloned by the runner (uid 0) but steps run as
		// jobUID — tell git the tree is safe so VCS stamping works.
		"-e", "GIT_CONFIG_COUNT=1",
		"-e", "GIT_CONFIG_KEY_0=safe.directory",
		"-e", "GIT_CONFIG_VALUE_0=*",
		"--entrypoint", "/bin/sh",
		e.claim.Job.Image, "-c", "while true; do sleep 30; done",
	)
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
	if e.privileged() {
		// Steps run as root; `sudo` only needs to pass the command through.
		// Written into the ephemeral container — the image stays untouched.
		_ = exec.CommandContext(ctx, "docker", "exec", e.container, "sh", "-c",
			`command -v sudo >/dev/null || { printf '#!/bin/sh\nexec "$@"\n' > /usr/local/bin/sudo && chmod +x /usr/local/bin/sudo; }`).Run()
		// docker-compose `./` volumes resolve client-side to /github/workspace
		// but the daemon mounts host-side. /github is a host dir bind-mounted
		// into the runner; point its workspace link at this job's workspace so
		// client paths land on the right host path. Race-free at concurrency 1.
		if dirExists("/github") {
			_ = os.Remove("/github/workspace")
			_ = os.Symlink(filepath.Join(e.workRoot(), "workspace"), "/github/workspace")
		}
	}
	return nil
}

// privileged reports whether this job runs with GitHub parity — root, a
// writable rootfs, and the docker socket. Maintainer-controlled trust levels
// ("trusted", "protected") opt in; fork/untrusted runs keep the sandbox.
func (e *executor) privileged() bool {
	return e.claim.Run.TrustLevel == "trusted" || e.claim.Run.TrustLevel == "protected"
}

// execUID is the uid steps run as inside the job container.
func (e *executor) execUID() int {
	if e.privileged() {
		return 0
	}
	return jobUID
}

// checkout materialises the repository inside the workspace directory. An
// uploaded bundle (kern exec) replaces the git clone entirely.
func (e *executor) checkout(ctx context.Context) error {
	ws := filepath.Join(e.workRoot(), "workspace")
	// A re-claimed job reuses the job-id work root; a dead attempt's residue
	// makes git clone refuse the non-empty dir. Workspace is ephemeral — wipe.
	if err := os.RemoveAll(ws); err != nil {
		return fmt.Errorf("clear stale workspace: %w", err)
	}
	if err := os.MkdirAll(ws, 0o777); err != nil {
		return fmt.Errorf("recreate workspace: %w", err)
	}
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
	args := cloneArgs(run, ws)
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

// cloneArgs builds the git command line. A clone token rides an extra header
// so it never lands in .git/config or in clone error output.
func cloneArgs(run ClaimRun, ws string) []string {
	var args []string
	if run.CloneToken != "" {
		// GitHub's git endpoint wants Basic auth: x-access-token:<token>.
		basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + run.CloneToken))
		args = append(args, "-c", "http.extraHeader=Authorization: Basic "+basic)
	}
	if run.SHA != "" {
		// A SHA checkout needs history, so no --depth.
		return append(args, "clone", "--quiet", "--", run.RepoURL, ws)
	}
	return append(args, "clone", "--quiet", "--depth", "1", "--", run.RepoURL, ws)
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
	e.stopServices(ctx)
	_ = os.RemoveAll(e.workRoot())
}

// chownTree gives every workspace entry to the job user. Best-effort: a
// runner without chown rights leaves the files as they are and the step
// surfaces its own EACCES diagnosis. Lchown keeps repository symlinks from
// reaching outside the workspace.
func chownTree(root string, uid int) {
	_ = filepath.Walk(root, func(p string, _ os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		_ = os.Lchown(p, uid, uid)
		return nil
	})
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

// serverURL derives the web origin actions expect in GITHUB_SERVER_URL from
// the run's clone URL; anything unparseable defaults to github.com.
func serverURL(repoURL string) string {
	u := strings.TrimPrefix(repoURL, "https://")
	u = strings.TrimPrefix(u, "http://")
	u = strings.TrimPrefix(u, "git@")
	u = strings.Replace(u, ":", "/", 1)
	host, _, _ := strings.Cut(u, "/")
	if host == "" {
		return "https://github.com"
	}
	return "https://" + host
}

// apiURL mirrors GITHUB_API_URL — api.github.com for github.com, /api/v3 on
// GHES-style hosts.
func apiURL(repoURL string) string {
	host := strings.TrimPrefix(serverURL(repoURL), "https://")
	if host == "github.com" {
		return "https://api.github.com"
	}
	return "https://" + host + "/api/v3"
}

// seedDockerCLI copies the runner's own docker client and any cli-plugins
// into dir (a host-visible work-root path) so privileged job containers can
// mount them. Idempotent — existing files win.
func seedDockerCLI(dir string) (string, error) {
	src, err := exec.LookPath("docker")
	if err != nil {
		return "", fmt.Errorf("docker cli not on runner PATH: %w", err)
	}
	src, _ = filepath.EvalSymlinks(src)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, "docker")
	if _, err := os.Stat(dst); os.IsNotExist(err) {
		data, err := os.ReadFile(src)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(dst, data, 0o755); err != nil {
			return "", err
		}
	}
	pluginDst := filepath.Join(dir, "cli-plugins")
	for _, cand := range []string{"/usr/libexec/docker/cli-plugins", "/usr/lib/docker/cli-plugins"} {
		if !dirExists(cand) {
			continue
		}
		entries, _ := os.ReadDir(cand)
		for _, en := range entries {
			to := filepath.Join(pluginDst, en.Name())
			if _, err := os.Stat(to); err == nil {
				continue
			}
			data, err := os.ReadFile(filepath.Join(cand, en.Name()))
			if err != nil {
				continue
			}
			_ = os.MkdirAll(pluginDst, 0o755)
			_ = os.WriteFile(to, data, 0o755)
		}
		break
	}
	return dir, nil
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

func tailStr(s string, n int) string {
	if len(s) <= n {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(s[len(s)-n:])
}
