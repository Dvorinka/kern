package runner

// Step execution: conditions, scripts, log streaming, file commands, outputs.

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/dvorinka/kern/internal/expressions"
	"github.com/dvorinka/kern/internal/secrets"
)

// runStep executes one step and returns its conclusion
// (success | failure | skipped | cancelled).
func (e *executor) runStep(ctx context.Context, step StepSpec) (string, error) {
	e.ann.reset()
	e.currentStep.Store(int32(step.Number))
	e.flushServiceLogs()
	if ok, err := e.stepCondition(step); err != nil {
		e.recordStep(step, "skipped", "skipped", nil)
		e.log.Warn("step condition error", zap.Int("step", step.Number), zap.Error(err))
		e.logStep(step, "KERN: step skipped — if: "+step.If+" — "+err.Error())
		_ = e.c.finishStep(context.Background(), e.jobID, step.Number, "skipped", 0, nil, nil)
		return "skipped", nil
	} else if !ok {
		e.recordStep(step, "skipped", "skipped", nil)
		e.logStep(step, "KERN: step skipped — if: "+step.If+" evaluated to false")
		_ = e.c.finishStep(context.Background(), e.jobID, step.Number, "skipped", 0, nil, nil)
		return "skipped", nil
	}

	if msg := e.preFailed[step.Number]; msg != "" {
		// Action resolution or its pre hook already failed at job setup — the
		// step fails without running main, like a failed GitHub prepare.
		e.logStep(step, "KERN: "+msg)
		e.recordStep(step, "failure", "failure", nil)
		_ = e.c.finishStep(context.Background(), e.jobID, step.Number, "failure", 1, nil, nil)
		return "failure", nil
	}

	// Operator-approved remediation for a requeued job runs before the step.
	if fix := e.claim.Remediations[strconv.Itoa(step.Number)]; len(fix) > 0 {
		e.logStep(step, "KERN: applying approved remediation")
		e.applyFix(ctx, step, fix)
	}

	if err := e.c.startStep(ctx, e.jobID, step.Number); err != nil {
		return "", fmt.Errorf("start step %d: %w", step.Number, err)
	}

	outcome, code, stepErr := e.runStepUnit(ctx, step)

	var diag *diagnosis
	if outcome == "failure" && !e.cancelled.Load() {
		errText := ""
		if stepErr != nil {
			errText = stepErr.Error()
		}
		diag = diagnose(step, code, e.lastTail, errText)
		// self_heal=auto: apply the allow-listed fix and retry the step once.
		if diag.Fixable && diag.Remediation != nil && e.claim.SelfHeal == "auto" {
			e.logStep(step, "KERN: self-heal auto — applying fix: "+strings.Join(diag.Remediation.Fix, "; "))
			if e.applyFix(ctx, step, diag.Remediation.Fix) {
				retry, retryCode, retryErr := e.runStepUnit(ctx, step)
				diag.Remediation.Applied = true
				if retry == "success" && retryErr == nil {
					diag.Remediation.Result = "step passed after fix"
					outcome, code = "success", retryCode
				} else {
					diag.Remediation.Result = "step still failing after fix"
				}
			} else {
				diag.Remediation.Result = "fix commands failed"
			}
		}
	}

	conclusion := outcome
	if e.cancelled.Load() {
		conclusion = "cancelled"
	} else if conclusion == "failure" && step.ContinueOnError {
		conclusion = "success" // outcome stays "failure" for downstream expressions
	}

	var outputs map[string]string
	if data, readErr := os.ReadFile(filepath.Join(e.cmdDir(), "GITHUB_OUTPUT")); readErr == nil {
		outputs, _ = parseKeyValue(string(data))
	}
	if data, readErr := os.ReadFile(filepath.Join(e.cmdDir(), "STEP_SUMMARY")); readErr == nil && len(data) > 0 {
		if err := e.c.uploadSummary(context.Background(), e.jobID, step.Number, data); err != nil {
			e.log.Warn("summary upload failed", zap.Int("step", step.Number), zap.Error(err))
		}
	}
	e.recordStep(step, conclusion, outcome, outputs)
	_ = e.c.finishStep(context.Background(), e.jobID, step.Number, conclusion, code, diag, e.ann.list())
	return conclusion, nil
}

// applyFix runs an allow-listed remediation plan as root inside the job
// container. Commands never contain secrets — they are fixed templates.
func (e *executor) applyFix(ctx context.Context, step StepSpec, fix []string) bool {
	for _, cmdline := range fix {
		var cmd *exec.Cmd
		if e.host {
			cmd = exec.CommandContext(ctx, "sh", "-c", cmdline)
			cmd.Dir = e.wsPath()
		} else {
			cmd = exec.CommandContext(ctx, "docker", "exec", "--user", "0:0",
				e.container, "sh", "-c", cmdline)
		}
		out, err := cmd.CombinedOutput()
		if len(out) > 0 {
			e.logStep(step, "KERN: fix: "+tailStr(string(out), 500))
		}
		if err != nil {
			return false
		}
	}
	return true
}

func (e *executor) recordStep(step StepSpec, conclusion, outcome string, outputs map[string]string) {
	e.stepResults[step.contextID()] = &stepOutcome{Conclusion: conclusion, Outcome: outcome, Outputs: outputs}
}

func (e *executor) cmdDir() string { return filepath.Join(e.workRoot(), "cmd") }

// runStepUnit runs a whole step unit: the script body (empty for uses steps,
// which also clears the file-command targets) then the action dispatch.
func (e *executor) runStepUnit(ctx context.Context, step StepSpec) (string, int, error) {
	// Step env values may carry expressions; resolve once here so the env
	// file, shell steps and action dispatches all see the same values.
	// Already-resolved values contain no ${{ and pass through untouched, so
	// the self-heal retry path is idempotent.
	if len(step.Env) > 0 {
		step.Env = e.interpolateEnv(step.Env)
	}
	outcome, code, stepErr := e.runStepBody(ctx, step)
	if stepErr != nil {
		e.logStep(step, "KERN: "+stepErr.Error())
		return outcome, code, stepErr
	}
	if step.Uses != "" {
		e.logStep(step, usesStepHeader(step))
		outcome, code = e.runUsesStep(ctx, step)
	}
	return outcome, code, nil
}

// runStepBody renders the script, launches docker exec and streams output.
func (e *executor) runStepBody(ctx context.Context, step StepSpec) (string, int, error) {
	script, shell, err := e.renderScript(step)
	if err != nil {
		return "failure", 1, err
	}

	stepCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	e.mu.Lock()
	e.killFn = cancel
	e.mu.Unlock()
	if e.cancelled.Load() {
		return "cancelled", 1, nil
	}
	if step.TimeoutSeconds > 0 {
		var stepCancel context.CancelFunc
		stepCtx, stepCancel = context.WithTimeout(stepCtx, time.Duration(step.TimeoutSeconds)*time.Second)
		defer stepCancel()
	}

	// Clear the file-command targets; the step appends fresh content. They are
	// then re-created and writable: @actions/core refuses to write a file
	// command that does not already exist, and job containers run as a
	// different uid than the runner host. PATH_LINES is repopulated with the
	// accumulated additions rather than emptied, since it is the only channel
	// the wrapper script has for prepending them.
	for _, f := range []string{"GITHUB_ENV", "GITHUB_OUTPUT", "STEP_SUMMARY", "GITHUB_STATE"} {
		p := filepath.Join(e.cmdDir(), f)
		_ = os.Remove(p)
		_ = os.WriteFile(p, nil, 0o666)
		_ = os.Chmod(p, 0o666)
	}
	_ = os.WriteFile(filepath.Join(e.cmdDir(), "PATH_LINES"), []byte(e.enc.pathLines()), 0o666)
	_ = os.Chmod(filepath.Join(e.cmdDir(), "PATH_LINES"), 0o666)

	if stepEnv := e.writeStepEnv(step); stepEnv == "" {
		return "failure", 1, fmt.Errorf("write step env file")
	}

	pump := newLogPump(e.c, e.jobID, step.Number, e.logMask, &e.ann)
	defer pump.close()
	if hdr := runStepHeader(step); hdr != "" {
		pump.write([]byte(hdr))
	}

	var cmd *exec.Cmd
	if e.host {
		cmd = e.hostStepCmd(stepCtx, step, shell, script)
	} else {
		execArgs := []string{"exec",
			"--user", fmt.Sprintf("%d:%d", e.execUID(), e.execUID()),
			"-w", containerWorkspace,
			"--env-file", e.stepEnvPath(step),
		}
		extraKeys := make([]string, 0, len(e.stepEnvExtra))
		for k := range e.stepEnvExtra {
			extraKeys = append(extraKeys, k)
		}
		sort.Strings(extraKeys)
		for _, k := range extraKeys {
			execArgs = append(execArgs, "-e", k+"="+e.stepEnvExtra[k])
		}
		execArgs = append(execArgs, e.container, shell, script)
		cmd = exec.CommandContext(stepCtx, "docker", execArgs...)
	}
	stdout, perr := cmd.StdoutPipe()
	if perr != nil {
		return "failure", 1, fmt.Errorf("stdout pipe: %w", perr)
	}
	stderr, perr := cmd.StderrPipe()
	if perr != nil {
		return "failure", 1, fmt.Errorf("stderr pipe: %w", perr)
	}
	if startErr := cmd.Start(); startErr != nil {
		if stepCtx.Err() != nil {
			return "cancelled", 1, nil
		}
		return "failure", 1, fmt.Errorf("launch step: %w", startErr)
	}

	doneCh := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done(); streamInto(stdout, pump) }()
		wg.Add(1)
		go func() { defer wg.Done(); streamInto(stderr, pump) }()
		wg.Wait()
		close(doneCh)
	}()
	<-doneCh

	waitErr := cmd.Wait() // streams already drained; exit code comes from ProcessState
	if waitErr != nil {
		pump.flush()
	}
	if e.cancelled.Load() {
		return "cancelled", exitValue(cmd), nil
	}
	if ctx.Err() == context.Canceled {
		return "cancelled", exitValue(cmd), nil
	}
	if stepCtx.Err() == context.DeadlineExceeded {
		return "failure", exitValue(cmd), fmt.Errorf("step timed out after %d seconds", step.TimeoutSeconds)
	}

	e.harvestEnv()
	e.lastTail = pump.tailCopy()
	code := exitValue(cmd)
	if code == 0 {
		return "success", 0, nil
	}
	return "failure", code, nil
}

// hostStepCmd runs a rendered step script as a local process on the runner
// host. Environment layering mirrors docker exec: the env file carries job
// env/secrets/persisted state, jobEnv carries the GITHUB_* contract and wins
// on conflicts (docker -e beats --env-file), stepEnvExtra wins last.
func (e *executor) hostStepCmd(ctx context.Context, step StepSpec, shell, script string) *exec.Cmd {
	// Start from the runner's own environment so steps inherit locale,
	// USER, SSH_AUTH_SOCK, toolchain PATH additions and friends — host
	// execution is a bare process, not a container.
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	if data, err := os.ReadFile(e.stepEnvPath(step)); err == nil {
		if pairs, perr := parseKeyValue(string(data)); perr == nil {
			for k, v := range pairs {
				env[k] = v
			}
		}
	}
	for k, v := range e.jobEnv() {
		env[k] = v
	}
	for k, v := range e.stepEnvExtra {
		env[k] = v
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	cmdEnv := make([]string, 0, len(keys))
	for _, k := range keys {
		cmdEnv = append(cmdEnv, k+"="+env[k])
	}
	cmd := exec.CommandContext(ctx, shell, script)
	cmd.Dir = e.wsPath()
	cmd.Env = cmdEnv
	return cmd
}

// runUsesStep dispatches a `uses` step to its runtime: native actions,
// local/remote composites, or a loud failure. Composite sub-step summaries
// accumulate in sumBuf and land back on the step's summary file for upload.
func (e *executor) runUsesStep(ctx context.Context, step StepSpec) (string, int) {
	e.sumBuf.Reset()
	outcome, code := e.runAction(ctx, step, e.runnerContext(), 0)
	if e.sumBuf.Len() > 0 {
		p := filepath.Join(e.cmdDir(), "STEP_SUMMARY")
		_ = os.WriteFile(p, e.sumBuf.Bytes(), 0o644)
	}
	return outcome, code
}

// harvestEnv folds GITHUB_ENV / GITHUB_PATH content into the env builder so later
// steps see values exported by earlier ones.
func (e *executor) harvestEnv() {
	if data, err := os.ReadFile(filepath.Join(e.cmdDir(), "GITHUB_ENV")); err == nil {
		if env, parseErr := parseKeyValue(string(data)); parseErr == nil {
			for k, v := range env {
				e.enc.persistKV(k, v)
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(e.cmdDir(), "PATH_LINES")); err == nil {
		e.enc.persistPaths(parsePathLines(string(data)))
	}
}

func exitValue(cmd *exec.Cmd) int {
	if cmd.ProcessState == nil {
		return 1
	}
	return cmd.ProcessState.ExitCode()
}

// stepCondition evaluates the step `if:` against runner progress.
func (e *executor) stepCondition(step StepSpec) (bool, error) {
	return expressions.EvaluateCondition(step.If, e.runnerContext())
}

// renderScript writes the bash wrapper and body; returns the wrapper path
// inside the container (/kern/scripts is mounted read-only) plus the resolved
// interpreter. Names carry a sequence so composite sub-steps sharing a step
// number never clobber each other.
func (e *executor) renderScript(step StepSpec) (string, string, error) {
	e.scriptSeq++
	tag := fmt.Sprintf("step-%d-%d", step.Number, e.scriptSeq)
	name := tag + ".sh"
	body := tag + ".body.sh"
	dir := filepath.Join(e.workRoot(), "scripts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	shell := e.shell
	if shell == "" {
		shell = "/bin/bash"
	}
	switch {
	case step.Shell == "sh":
		shell = "/bin/sh"
	case strings.HasPrefix(step.Shell, "/"):
		shell = step.Shell // composite steps carry resolved interpreter paths
	}
	pipefail := "set -eo pipefail\n"
	if filepath.Base(shell) != "bash" {
		pipefail = "set -e\n" // pipefail is a bashism
	}
	cmdEnv, scriptBase := e.cmdEnvPath(), e.scriptDirPath()
	wrapper := "#!" + shell + "\n" +
		pipefail +
		"if [ -f " + cmdEnv + "/PATH_LINES ]; then\n" +
		"  while IFS= read -r p; do export PATH=\"$p:$PATH\"; done < " + cmdEnv + "/PATH_LINES\n" +
		"fi\n" +
		"export GITHUB_ENV=" + cmdEnv + "/GITHUB_ENV\n" +
		"export GITHUB_PATH=" + cmdEnv + "/PATH_LINES\n" +
		"export GITHUB_OUTPUT=" + cmdEnv + "/GITHUB_OUTPUT\n" +
		"export GITHUB_STEP_SUMMARY=" + cmdEnv + "/STEP_SUMMARY\n" +
		"export GITHUB_STATE=" + cmdEnv + "/GITHUB_STATE\n" +
		"cd " + shellQuote(e.wsPath()) + "\n"
	if wd := strings.TrimSpace(step.WorkingDirectory); wd != "" {
		wrapper += "cd " + shellQuote(wd) + "\n"
	}
	// The body is a new shell, so the wrapper's pipefail does not cover it.
	// Without it, `gofmt | test` stays green when gofmt is missing.
	bodyFlags := " -e "
	if filepath.Base(shell) == "bash" {
		bodyFlags = " -eo pipefail "
	}
	wrapper += shell + bodyFlags + shellQuote(filepath.Join(scriptBase, body)) + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(wrapper), 0o755); err != nil {
		return "", "", err
	}
	ctx := e.exprCtx
	if ctx == nil {
		ctx = e.runnerContext()
	}
	bodyText, err := expressions.Interpolate(step.Run, ctx)
	if err != nil {
		return "", "", fmt.Errorf("interpolate run script: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, body), []byte(bodyText), 0o644); err != nil {
		return "", "", err
	}
	return filepath.Join(scriptBase, name), shell, nil
}

// writeStepEnv builds the per-step --env-file with job env, secrets, step env and
// persisted state. It returns the host path used by docker exec.
func (e *executor) writeStepEnv(step StepSpec) string {
	// Job env reaches the process env through the env file; hand the builder
	// the expression-resolved values, not the raw ${{ }} templates.
	e.enc.jobEnv = e.resolvedJobEnv(e.baseContext())
	path := e.stepEnvPath(step)
	content, extra := e.enc.render(step)
	e.stepEnvExtra = extra
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return ""
	}
	return path
}

func (e *executor) stepEnvPath(step StepSpec) string {
	return filepath.Join(e.workRoot(), "scripts", fmt.Sprintf("step-%d.env", step.Number))
}

// evaluateOutputs resolves the job outputs spec using the expression context.
func (e *executor) evaluateOutputs(string) map[string]string {
	if len(e.claim.Job.OutputsSpec) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, tmpl := range e.claim.Job.OutputsSpec {
		v, err := expressions.Interpolate(tmpl, e.runnerContext())
		if err != nil {
			e.log.Warn("job output expression failed", zap.String("output", k), zap.Error(err))
			continue
		}
		out[k] = v
	}
	return out
}

// runnerContext builds the expression context available to step conditions and outputs.
// needs.* is not resolvable on the runner; the control plane evaluates job conditions.
func (e *executor) runnerContext() map[string]any {
	ctx := e.baseContext()
	// Job env values may carry expressions (env: FOO: ${{ secrets.X }});
	// resolve them once against the non-env contexts and expose the result
	// both as process env (envBuilder) and as the env expression context.
	env := map[string]any{}
	for k, v := range e.resolvedJobEnv(ctx) {
		env[k] = v
	}
	ctx["env"] = env
	return ctx
}

// baseContext is runnerContext minus the env key: the context job-level env
// values are allowed to see (no env self-reference, no step outputs yet).
func (e *executor) baseContext() map[string]any {
	steps := map[string]any{}
	for id, r := range e.stepResults {
		outs := map[string]any{}
		for k, v := range r.Outputs {
			outs[k] = v
		}
		steps[id] = map[string]any{
			"outcome": r.Outcome, "conclusion": r.Conclusion, "outputs": outs,
		}
	}
	matrix := map[string]any{}
	for k, v := range e.claim.Job.Matrix {
		matrix[k] = v
	}
	github := map[string]any{
		"event_name": e.claim.Run.Event, "ref": e.claim.Run.Ref, "sha": e.claim.Run.SHA,
		"actor": e.claim.Run.Actor, "run_id": e.claim.Run.ID,
		"run_number": float64(e.claim.Run.Number), "workspace": e.wsPath(),
		"repository": e.claim.Run.RepoFullName, "job": e.claim.Job.Key,
		"workflow": e.claim.Run.Workflow, "head_ref": e.claim.Run.HeadRef,
		"head_sha": e.claim.Run.HeadSHA, "event": e.eventPayload,
	}
	needs := e.claim.Job.Needs
	if needs == nil {
		needs = map[string]any{}
	}
	inputs := map[string]any{}
	for k, v := range e.claim.Job.Inputs {
		inputs[k] = v
	}
	secretCtx := map[string]any{}
	for k, v := range e.secrets {
		secretCtx[k] = v
	}
	varCtx := map[string]any{}
	for k, v := range e.vars {
		varCtx[k] = v
	}
	ctx := map[string]any{
		"github": github, "matrix": matrix, "steps": steps, "needs": needs,
		"inputs": inputs, "secrets": secretCtx, "vars": varCtx,
		"runner": e.runnerCtxMap(), "job": map[string]any{},
		"__job_status": e.jobStatusSoFar(),
		"__workspace":  filepath.Join(e.workRoot(), "workspace"),
	}
	return ctx
}

// interpolateEnv resolves ${{ }} expressions in a step env map. Values that
// fail to interpolate keep their literal text — a bad expression should not
// silently drop the variable.
func (e *executor) interpolateEnv(env map[string]string) map[string]string {
	ctx := e.runnerContext()
	out := make(map[string]string, len(env))
	for k, v := range env {
		if strings.Contains(v, "${{") {
			if r, err := expressions.Interpolate(v, ctx); err == nil {
				v = r
			}
		}
		out[k] = v
	}
	return out
}

// resolvedJobEnv interpolates claim.Job.Env values once per job. Job-level
// env is defined before any step runs, so the context deliberately excludes
// env/steps — matching GitHub, where job env cannot see step outputs.
func (e *executor) resolvedJobEnv(ctx map[string]any) map[string]string {
	if e.jobEnvResolved != nil {
		return e.jobEnvResolved
	}
	out := map[string]string{}
	for k, v := range e.claim.Job.Env {
		if strings.Contains(v, "${{") {
			if r, err := expressions.Interpolate(v, ctx); err == nil {
				v = r
			}
		}
		out[k] = v
	}
	e.jobEnvResolved = out
	return out
}

// runnerContext fills GitHub's `runner` context with the values job
// containers actually see. Steps like dtolnay/rust-toolchain branch on
// runner.os inside bash; an empty map interpolates to nothing and breaks
// the script's syntax.
// runnerCtxMap is runnerContext plus mode-aware filesystem paths: on the
// host executor steps see host paths, inside containers the /kern mounts.
func (e *executor) runnerCtxMap() map[string]any {
	rc := runnerContext(e.cfg.Name)
	if e.host {
		rc["tool_cache"] = e.cachePath() + "/_toolcache"
		rc["temp"] = e.tmpPath()
		rc["workspace"] = e.wsPath()
	}
	return rc
}

func runnerContext(name string) map[string]any {
	if name == "" {
		name, _ = os.Hostname()
	}
	return map[string]any{
		"os":          runnerGOOS(),
		"arch":        runnerArch(),
		"name":        name,
		"environment": "self-hosted",
		"tool_cache":  containerCacheDir + "/_toolcache",
		"temp":        containerTmpDir,
		"workspace":   containerWorkspace,
		"debug":       "false",
	}
}

func runnerGOOS() string {
	switch runtime.GOOS {
	case "linux":
		return "Linux"
	case "windows":
		return "Windows"
	case "darwin":
		return "macOS"
	}
	return runtime.GOOS
}

func runnerArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "X64"
	case "arm64":
		return "ARM64"
	case "386":
		return "X86"
	case "arm":
		return "ARM"
	}
	return runtime.GOARCH
}

// jobStatusSoFar reflects the first failure/cancel so failure() and cancelled()
// behave like GitHub for subsequent step `if:` expressions.
func (e *executor) jobStatusSoFar() string {
	status := ""
	for _, r := range e.stepResults {
		switch r.Conclusion {
		case "failure", "cancelled":
			return r.Conclusion
		}
	}
	return status
}

// logPump streams captured output to the control plane in batches. A flush
// goes out on whichever comes first — 256 KiB buffered or the flush interval
// elapsing — so a quiet step costs one POST and a loud one stays live without
// hammering appendLog. Masking happens server-side (secrets.Masker).
//
// stdout and stderr stream on separate goroutines; mu guards buf and tail.
type logPump struct {
	c     *client
	jobID string
	step  int
	// mask covers credentials the server-side masker cannot know (the
	// per-run clone and GITHUB_TOKENs). Nil means passthrough.
	mask *secrets.Masker
	// anns collects ::error::/::warning:: workflow commands seen on this
	// step's streams; nil disables parsing.
	anns *annotationSink

	mu     sync.Mutex
	buf    []byte
	tail   []byte // last bytes seen, for failure diagnosis
	stop   chan struct{}
	closed sync.WaitGroup
}

const (
	logFlushBytes    = 256 << 10
	logFlushInterval = time.Second
)

func newLogPump(c *client, jobID string, step int, mask *secrets.Masker, anns *annotationSink) *logPump {
	p := &logPump{c: c, jobID: jobID, step: step, mask: mask, anns: anns, stop: make(chan struct{})}
	p.closed.Add(1)
	go func() {
		defer p.closed.Done()
		t := time.NewTicker(logFlushInterval)
		defer t.Stop()
		for {
			select {
			case <-p.stop:
				return
			case <-t.C:
				p.flush()
			}
		}
	}()
	return p
}

// write buffers a chunk (a full line); stored secrets are masked server-side,
// per-run tokens are masked here before they can reach the wire.
func (p *logPump) write(line []byte) {
	if p.mask != nil {
		line = p.mask.MaskBytes(line)
	}
	if p.anns != nil {
		if a := parseAnnotation(line); a != nil {
			p.anns.add(*a)
		}
	}
	p.mu.Lock()
	p.buf = append(p.buf, line...)
	p.tail = append(p.tail, line...)
	if len(p.tail) > 8<<10 {
		p.tail = p.tail[len(p.tail)-8<<10:]
	}
	full := len(p.buf) >= logFlushBytes
	p.mu.Unlock()
	if full {
		p.flush() // applies backpressure on very loud steps
	}
}

func (p *logPump) flush() {
	p.mu.Lock()
	if len(p.buf) == 0 {
		p.mu.Unlock()
		return
	}
	data := p.buf
	p.buf = nil
	p.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := p.c.appendLog(ctx, p.jobID, p.step, data); err != nil {
		// Losing a chunk is acceptable; the next flush retries with fresh bytes.
		buf := make([]byte, 0, len(data)+64)
		lines := strings.SplitN(string(data), "\n", 2)
		if len(lines) == 2 {
			buf = append(buf, []byte(lines[0])...)
			buf = append(buf, '\n')
			_ = p.c.appendLog(context.Background(), p.jobID, p.step, buf)
		}
	}
}

// tailCopy returns the trailing log bytes collected so far.
func (p *logPump) tailCopy() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return string(p.tail)
}

func (p *logPump) close() {
	close(p.stop)
	p.closed.Wait()
	p.flush()
}

// streamInto copies a reader into the pump line by line.
func streamInto(r io.Reader, p *logPump) {
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			p.write([]byte(line))
		}
		if err != nil {
			if err != io.EOF && len(line) > 0 {
				p.write([]byte("\n")) // terminate a partial trailing line
			}
			return
		}
	}
}

// envBuilder assembles the per-step env file content.
type envBuilder struct {
	mu     sync.Mutex
	jobEnv map[string]string
	// secrets never enter the log stream; they are env-file only.
	secrets map[string]string
	persist map[string]string
	paths   []string
	pathSet map[string]bool
}

func newEnvBuilder(claim *Claim, secrets map[string]string) *envBuilder {
	b := &envBuilder{jobEnv: map[string]string{}, secrets: secrets, persist: map[string]string{}}
	for k, v := range claim.Job.Env {
		b.jobEnv[k] = v
	}
	return b
}

func (b *envBuilder) persistKV(k, v string) {
	b.mu.Lock()
	b.persist[k] = v
	b.mu.Unlock()
}

func (b *envBuilder) persistPaths(paths []string) {
	b.mu.Lock()
	if b.pathSet == nil {
		b.pathSet = map[string]bool{}
		for _, p := range b.paths {
			b.pathSet[p] = true
		}
	}
	for _, p := range paths {
		if strings.TrimSpace(p) != "" && !b.pathSet[p] {
			b.pathSet[p] = true
			b.paths = append(b.paths, p)
		}
	}
	b.mu.Unlock()
}

// pathLines renders the accumulated PATH additions oldest-first; the step
// wrapper prepends each line, so the newest entry ends up first. A trailing
// newline is required: actions append to this file, and a missing newline
// glues the next directory onto the previous one.
func (b *envBuilder) pathLines() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.paths) == 0 {
		return ""
	}
	return strings.Join(b.paths, "\n") + "\n"
}

// render returns the env-file content for a step plus the vars that must ride
// `-e` args instead: docker's --env-file parser treats every line as
// KEY=value, so multi-line values (pretty-printed service-account JSON, PEM
// keys, heredoc exports) would corrupt the file. Values with spaces but no
// newline are safe — docker keeps everything after the first `=` verbatim.
// Extra vars are visible in the container's inspect output, matching GitHub's
// own visibility for env.
func (b *envBuilder) render(step StepSpec) (string, map[string]string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out strings.Builder
	extra := map[string]string{}
	writeKV := func(k, v string) {
		if k == "" || strings.ContainsAny(k, " =\n") {
			return
		}
		v = strings.TrimRight(v, "\n")
		if strings.Contains(v, "\n") {
			extra[k] = v
			return
		}
		// A later layer resolved this key to a file-safe value: drop any
		// earlier extra entry or the stale `-e` arg would override it.
		delete(extra, k)
		fmt.Fprintf(&out, "%s=%s\n", k, v)
	}
	for k, v := range b.jobEnv {
		writeKV(k, v)
	}
	for k, v := range b.persist {
		writeKV(k, v)
	}
	for k, v := range b.secrets {
		writeKV(k, v)
	}
	for k, v := range step.Env {
		writeKV(k, v)
	}
	return out.String(), extra
}

// parseKeyValue reads GitHub file-command content: `KEY=value` lines plus
// heredoc blocks (`KEY<<DELIM` … `DELIM`). @actions/core ≥1.11 emits the
// heredoc form for every export, so both shapes must parse or env written by
// modern node actions (ANDROID_HOME, JAVA_HOME, step outputs, STATE_*) drops
// silently. Heredoc values keep inner newlines verbatim.
func parseKeyValue(s string) (map[string]string, error) {
	out := map[string]string{}
	lines := strings.Split(s, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if key, delim, ok := cutHeredoc(line); ok {
			var val strings.Builder
			for i++; i < len(lines); i++ {
				if strings.TrimRight(lines[i], "\r") == delim {
					break
				}
				val.WriteString(lines[i])
				val.WriteByte('\n')
			}
			out[key] = strings.TrimSuffix(val.String(), "\n")
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = v
	}
	return out, nil
}

// cutHeredoc recognises a `KEY<<DELIM` file-command opener. The key must not
// contain `=` or whitespace so `A=B<<C` still parses as a plain assignment.
func cutHeredoc(line string) (key, delim string, ok bool) {
	k, d, found := strings.Cut(line, "<<")
	if !found {
		return "", "", false
	}
	k, d = strings.TrimSpace(k), strings.TrimSpace(d)
	if k == "" || d == "" || strings.ContainsAny(k, "= \t") {
		return "", "", false
	}
	return k, d, true
}

// parsePathLines extracts directories from GITHUB_PATH content: bare lines
// (`echo "dir" >> $GITHUB_PATH`, and KERN's own persisted entries) plus
// heredoc blocks emitted by @actions/core addPath.
func parsePathLines(s string) []string {
	var out []string
	lines := strings.Split(s, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if _, delim, ok := cutHeredoc(line); ok {
			for i++; i < len(lines); i++ {
				if strings.TrimRight(lines[i], "\r") == delim {
					break
				}
				if v := strings.TrimSpace(lines[i]); v != "" {
					out = append(out, v)
				}
			}
			continue
		}
		out = append(out, line)
	}
	return out
}

// shellQuote applies minimal POSIX shell quoting to a parameter.
func shellQuote(v string) string {
	return "'" + strings.ReplaceAll(v, "'", `'"'"'`) + "'"
}

// runStepHeader renders the GitHub-style group block shown above a `run:`
// step's output. Without it a silent successful step (go build, mkdir)
// produces zero log bytes and the UI shows "No log output yet" — which reads
// as broken rather than as a quiet success. step.Run is the raw YAML script;
// expressions stay literal, matching what GitHub echoes, and secret values
// are masked server-side at ingestion.
func runStepHeader(step StepSpec) string {
	script := strings.TrimSpace(step.Run)
	if script == "" {
		return ""
	}
	return fmt.Sprintf("##[group]Run %s\n%s\n##[endgroup]\n", firstLine(script), script)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// usesStepHeader renders the same group block for native `uses:` steps so
// checkout/cache/artifact steps show what ran, matching `run:` steps.
func usesStepHeader(step StepSpec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "##[group]Run %s\n", step.Uses)
	if len(step.With) > 0 {
		b.WriteString("with:\n")
		keys := make([]string, 0, len(step.With))
		for k := range step.With {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "  %s: %v\n", k, step.With[k])
		}
	}
	b.WriteString("##[endgroup]\n")
	return b.String()
}
