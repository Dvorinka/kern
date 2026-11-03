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
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/dvorinka/kern/internal/expressions"
)

// runStep executes one step and returns its conclusion
// (success | failure | skipped | cancelled).
func (e *executor) runStep(ctx context.Context, step StepSpec) (string, error) {
	if ok, err := e.stepCondition(step); err != nil {
		e.recordStep(step, "skipped", "skipped", nil)
		e.log.Warn("step condition error", zap.Int("step", step.Number), zap.Error(err))
		_ = e.c.finishStep(context.Background(), e.jobID, step.Number, "skipped", 0)
		return "skipped", nil
	} else if !ok {
		e.recordStep(step, "skipped", "skipped", nil)
		_ = e.c.finishStep(context.Background(), e.jobID, step.Number, "skipped", 0)
		return "skipped", nil
	}

	if err := e.c.startStep(ctx, e.jobID, step.Number); err != nil {
		return "", fmt.Errorf("start step %d: %w", step.Number, err)
	}

	outcome, code, stepErr := e.runStepBody(ctx, step)
	if stepErr == nil && step.Uses != "" {
		outcome, code = e.runUsesStep(ctx, step)
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
	e.recordStep(step, conclusion, outcome, outputs)
	_ = e.c.finishStep(context.Background(), e.jobID, step.Number, conclusion, code)
	return conclusion, nil
}

func (e *executor) recordStep(step StepSpec, conclusion, outcome string, outputs map[string]string) {
	e.stepResults[step.contextID()] = &stepOutcome{Conclusion: conclusion, Outcome: outcome, Outputs: outputs}
}

func (e *executor) cmdDir() string { return filepath.Join(e.workRoot(), "cmd") }

// runStepBody renders the script, launches docker exec and streams output.
func (e *executor) runStepBody(ctx context.Context, step StepSpec) (string, int, error) {
	script, err := e.renderScript(step)
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

	// Clear the file-command targets; the step appends fresh content.
	for _, f := range []string{"GITHUB_ENV", "PATH_LINES", "GITHUB_OUTPUT", "STEP_SUMMARY"} {
		_ = os.Remove(filepath.Join(e.cmdDir(), f))
	}

	if stepEnv := e.writeStepEnv(step); stepEnv == "" {
		return "failure", 1, fmt.Errorf("write step env file")
	}

	pump := newLogPump(e.c, e.jobID, step.Number)
	defer pump.close()

	cmd := exec.CommandContext(stepCtx, "docker", "exec",
		"--user", fmt.Sprintf("%d:%d", jobUID, jobUID),
		"-w", containerWorkspace,
		"--env-file", e.stepEnvPath(step),
		e.container, "/bin/bash", script,
	)
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
	code := exitValue(cmd)
	if code == 0 {
		return "success", 0, nil
	}
	return "failure", code, nil
}

// runUsesStep supports actions/checkout implicitly and fails explicit unsupported
// actions with an explicit diagnostic (compatibility.md guarantees loud, not silent).
func (e *executor) runUsesStep(_ context.Context, step StepSpec) (string, int) {
	ref := strings.TrimSpace(step.Uses)
	if ref == "actions/checkout" || strings.HasPrefix(ref, "actions/checkout@") {
		// The workspace was checked out during prepare; an explicit step keeps that state.
		return "success", 0
	}
	// ponytail: composite/JS/Docker action runtimes land in M5; fail loudly.
	e.log.Warn("action kind unavailable", zap.Int("step", step.Number), zap.String("uses", ref))
	diag := fmt.Sprintf("KERN: action %q is not supported yet (action runtimes ship in a later phase). "+
		"The step fails by design rather than being silently ignored; see docs/compatibility.md.", ref)
	_ = e.c.appendLog(context.Background(), e.jobID, step.Number, []byte(diag+"\n"))
	return "failure", 1
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
		e.enc.persistPaths(strings.Split(strings.TrimRight(string(data), "\n"), "\n"))
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
	if strings.TrimSpace(step.If) == "" {
		return true, nil
	}
	return expressions.EvaluateCondition(step.If, e.runnerContext())
}

// renderScript writes the bash wrapper and body; returns the wrapper path inside the
// container (/kern/scripts is mounted read-only).
func (e *executor) renderScript(step StepSpec) (string, error) {
	name := fmt.Sprintf("step-%d.sh", step.Number)
	body := fmt.Sprintf("step-%d.body.sh", step.Number)
	dir := filepath.Join(e.workRoot(), "scripts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	wrapper := "#!/bin/bash\n" +
		"set -eo pipefail\n" +
		"if [ -f " + containerCmdDir + "/PATH_LINES ]; then\n" +
		"  while IFS= read -r p; do export PATH=\"$p:$PATH\"; done < " + containerCmdDir + "/PATH_LINES\n" +
		"fi\n" +
		"export GITHUB_ENV=" + containerCmdDir + "/GITHUB_ENV\n" +
		"export GITHUB_PATH=" + containerCmdDir + "/PATH_LINES\n" +
		"export GITHUB_OUTPUT=" + containerCmdDir + "/GITHUB_OUTPUT\n" +
		"export GITHUB_STEP_SUMMARY=" + containerCmdDir + "/STEP_SUMMARY\n" +
		"cd " + shellQuote(containerWorkspace) + "\n"
	if wd := strings.TrimSpace(step.WorkingDirectory); wd != "" {
		wrapper += "cd " + shellQuote(wd) + "\n"
	}
	wrapper += "bash -e " + shellQuote(filepath.Join(containerScriptDir, body)) + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(wrapper), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, body), []byte(step.Run), 0o644); err != nil {
		return "", err
	}
	return filepath.Join(containerScriptDir, name), nil
}

// writeStepEnv builds the per-step --env-file with job env, secrets, step env and
// persisted state. It returns the host path used by docker exec.
func (e *executor) writeStepEnv(step StepSpec) string {
	path := e.stepEnvPath(step)
	if err := os.WriteFile(path, []byte(e.enc.render(step)), 0o600); err != nil {
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
	steps := map[string]any{}
	for id, r := range e.stepResults {
		outs := r.Outputs
		if outs == nil {
			outs = map[string]string{}
		}
		steps[id] = map[string]any{
			"outcome": r.Outcome, "conclusion": r.Conclusion, "outputs": outs,
		}
	}
	matrix := map[string]any{}
	for k, v := range e.claim.Job.Matrix {
		matrix[k] = v
	}
	env := map[string]any{}
	for k, v := range e.claim.Job.Env {
		env[k] = v
	}
	github := map[string]any{
		"event_name": e.claim.Run.Event, "ref": e.claim.Run.Ref, "sha": e.claim.Run.SHA,
		"actor": e.claim.Run.Actor, "run_id": e.claim.Run.ID,
		"run_number": float64(e.claim.Run.Number), "workspace": containerWorkspace,
		"repository": e.claim.Run.RepoFullName, "job": e.claim.Job.Key,
		"workflow": e.claim.Run.Workflow,
	}
	return map[string]any{
		"github": github, "matrix": matrix, "env": env, "steps": steps,
		"runner": map[string]any{}, "job": map[string]any{},
		"__job_status": e.jobStatusSoFar(),
	}
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

// logPump streams captured output to the control plane in line chunks.
// Masking happens server-side at ingestion (secrets.Masker).
type logPump struct {
	c     *client
	jobID string
	step  int
	buf   []byte
}

func newLogPump(c *client, jobID string, step int) *logPump {
	return &logPump{c: c, jobID: jobID, step: step}
}

// write buffers a chunk (a full line); a masker would run before render on the server.
func (p *logPump) write(line []byte) {
	p.buf = append(p.buf, line...)
	if len(p.buf) >= 64<<10 {
		p.flush()
	}
}

func (p *logPump) flush() {
	if len(p.buf) == 0 {
		return
	}
	data := p.buf
	p.buf = nil
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

func (p *logPump) close() { p.flush() }

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
	secrets  map[string]string
	persist  map[string]string
	paths    []string
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
	for _, p := range paths {
		if strings.TrimSpace(p) != "" {
			b.paths = append(b.paths, p)
		}
	}
	b.mu.Unlock()
}

// render returns the env-file content for a step.
func (b *envBuilder) render(step StepSpec) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out strings.Builder
	writeKV := func(k, v string) {
		if k == "" || strings.ContainsAny(k, " =\n") {
			return
		}
		v = strings.TrimRight(v, "\n") // multi-line values are valid env-file content
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
	return out.String()
}

// parseKeyValue reads `KEY=value` lines (the file-command format).
func parseKeyValue(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
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

// shellQuote applies minimal POSIX shell quoting to a parameter.
func shellQuote(v string) string {
	return "'" + strings.ReplaceAll(v, "'", `'"'"'`) + "'"
}
