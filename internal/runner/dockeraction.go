package runner

// Docker action runtime. `uses: docker://image` runs the image as a sibling
// container on the runner host; `runs.using: docker` actions either build
// their Dockerfile or pull a published image. Action containers get the same
// hardening as job containers (dropped caps, no-new-privileges, read-only
// root, resource limits) plus mounts for the workspace, the file-command dir
// and a writable home. Logs stream to the step; GITHUB_* file commands flow
// through the shared /github/cmd mount.
//
// ponytail: pre-entrypoint/post-entrypoint are ignored (logged once); upgrade
// when a docker action needs them.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dvorinka/kern/internal/actions"
	"github.com/dvorinka/kern/internal/expressions"
)

const containerActionHome = "/github/home"

// runDockerRef handles `uses: docker://image[:tag]` — no action.yml, the
// image's own entrypoint runs with the `with:` keys as INPUT_* and args.
func (e *executor) runDockerRef(ctx context.Context, step StepSpec) (string, int) {
	image := strings.TrimPrefix(strings.TrimSpace(step.Uses), "docker://")
	if !imageRe.MatchString(image) {
		e.logStep(step, "KERN: invalid docker image reference "+step.Uses)
		return "failure", 1
	}
	env := map[string]string{}
	for k, v := range step.Env {
		env[k] = v
	}
	for k, w := range step.With {
		env[inputKey(k)] = envString(w)
	}
	var args []string
	if a := withString(step, "args", ""); a != "" {
		args = strings.Fields(a)
	}
	outcome, code := e.dockerActionRun(ctx, step, image, "", args, env)
	if outcome != "cancelled" {
		recordImageUse(e.cfg.WorkRoot, image)
	}
	return outcome, code
}

// runDockerAction handles runs.using: docker actions: build a Dockerfile in
// the action directory or pull the named image, then run it with the
// action's entrypoint/args contract.
func (e *executor) runDockerAction(ctx context.Context, step StepSpec, meta *actions.Meta, hostDir, actionPath string, evalCtx map[string]any) (string, int) {
	if meta.Runs.PreEntrypoint != "" || meta.Runs.PostEntrypoint != "" {
		e.logStep(step, "KERN: warning: pre/post-entrypoint are not executed yet")
	}
	image := strings.TrimSpace(meta.Runs.Image)
	var err error
	switch {
	case image == "":
		e.logStep(step, "KERN: "+step.Uses+" declares runs.using: docker without runs.image")
		return "failure", 1
	case strings.EqualFold(image, "Dockerfile"):
		tag := fmt.Sprintf("kern-action-%s-%d", sanitizeID(e.claim.Job.ID), step.Number)
		if err = e.docker(ctx, "build", "-q", "-t", tag, hostDir); err != nil {
			e.logStep(step, "KERN: docker build "+hostDir+": "+err.Error())
			return "failure", 1
		}
		image = tag
	case !imageRe.MatchString(image):
		e.logStep(step, "KERN: invalid runs.image "+image)
		return "failure", 1
	default:
		if err = e.docker(ctx, "pull", "-q", image); err != nil {
			e.logStep(step, "KERN: docker pull "+image+": "+err.Error())
			return "failure", 1
		}
		recordImageUse(e.cfg.WorkRoot, image)
	}
	env := map[string]string{}
	for k, v := range step.Env {
		env[k] = v
	}
	env["GITHUB_ACTION_PATH"] = actionPath
	for k, w := range step.With {
		env[inputKey(k)] = envString(w)
	}
	for k, v := range meta.Runs.Env {
		env[k] = envString(v)
	}
	inputs, _, ierr := resolveInputs(meta, step.With)
	if ierr != nil {
		e.logStep(step, "KERN: "+ierr.Error())
		return "failure", 1
	}
	argsCtx := compositeCtx(evalCtx, inputs, nil, e.jobStatusSoFar())
	var args []string
	for _, a := range meta.Runs.Args {
		s, err := expressions.Interpolate(a, argsCtx)
		if err != nil {
			e.logStep(step, "KERN: args interpolation: "+err.Error())
			return "failure", 1
		}
		args = append(args, s)
	}
	if a := withString(step, "args", ""); a != "" {
		args = append(args, strings.Fields(a)...)
	}
	return e.dockerActionRun(ctx, step, image, meta.Runs.Entrypoint, args, env)
}

// dockerActionRun starts the action image with job-container hardening and
// streams its output into the step log. File commands land in /github/cmd
// and are harvested by the caller's normal flow.
func (e *executor) dockerActionRun(ctx context.Context, step StepSpec, image, entrypoint string, args []string, env map[string]string) (string, int) {
	home := filepath.Join(e.workRoot(), "action-home")
	for _, d := range []string{home} {
		if err := os.MkdirAll(d, 0o777); err != nil {
			return "failure", 1
		}
	}
	if stepEnv := e.writeStepEnv(step); stepEnv == "" {
		e.logStep(step, "KERN: write step env file")
		return "failure", 1
	}
	if step.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(step.TimeoutSeconds)*time.Second)
		defer cancel()
	}
	name := fmt.Sprintf("kern-action-%s-%d-%d", sanitizeID(e.claim.Job.ID), step.Number, e.scriptSeq+1)
	dargs := []string{
		"run", "--rm", "--name", name,
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--read-only",
		"--tmpfs", "/tmp:rw,noexec,nosuid,size=512m",
		"--memory", valueOr(e.cfg.JobMemory, "1g"),
		"--cpus", valueOr(e.cfg.JobCPUs, "1"),
		"-v", filepath.Join(e.workRoot(), "workspace") + ":" + containerWorkspace,
		"-v", e.cmdDir() + ":" + containerCmdDir,
		"-v", filepath.Join(e.workRoot(), "actions") + ":" + containerActionsDir + ":ro",
		"-v", home + ":" + containerActionHome,
		"-w", containerWorkspace,
		"--env-file", e.stepEnvPath(step),
		"-e", "HOME=" + containerActionHome,
		"-e", "GITHUB_WORKSPACE=" + containerWorkspace,
		"-e", "GITHUB_ACTION_PATH=" + env["GITHUB_ACTION_PATH"],
		"-e", "GITHUB_ENV=" + containerCmdDir + "/GITHUB_ENV",
		"-e", "GITHUB_PATH=" + containerCmdDir + "/PATH_LINES",
		"-e", "GITHUB_OUTPUT=" + containerCmdDir + "/GITHUB_OUTPUT",
		"-e", "GITHUB_STEP_SUMMARY=" + containerCmdDir + "/STEP_SUMMARY",
		"-e", "GIT_CONFIG_COUNT=1",
		"-e", "GIT_CONFIG_KEY_0=safe.directory",
		"-e", "GIT_CONFIG_VALUE_0=*",
	}
	for k, v := range env {
		dargs = append(dargs, "-e", k+"="+v)
	}
	if entrypoint != "" {
		dargs = append(dargs, "--entrypoint", entrypoint)
	}
	dargs = append(dargs, image)
	dargs = append(dargs, args...)

	pump := newLogPump(e.c, e.jobID, step.Number)
	defer pump.close()
	stepCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	e.mu.Lock()
	e.killFn = cancel
	e.mu.Unlock()
	cmd := exec.CommandContext(stepCtx, "docker", dargs...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "failure", 1
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "failure", 1
	}
	if err := cmd.Start(); err != nil {
		e.logStep(step, "KERN: docker run "+image+": "+err.Error())
		return "failure", 1
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); streamInto(stdout, pump) }()
	go func() { defer wg.Done(); streamInto(stderr, pump) }()
	wg.Wait()
	waitErr := cmd.Wait()
	if waitErr != nil {
		pump.flush()
	}
	e.harvestEnv()
	e.lastTail = pump.tailCopy()
	if e.cancelled.Load() || stepCtx.Err() == context.Canceled && waitErr != nil {
		return "cancelled", exitValue(cmd)
	}
	if stepCtx.Err() == context.DeadlineExceeded {
		e.logStep(step, fmt.Sprintf("KERN: action timed out after %d seconds", step.TimeoutSeconds))
		return "failure", exitValue(cmd)
	}
	code := exitValue(cmd)
	if code == 0 {
		return "success", 0
	}
	return "failure", code
}
