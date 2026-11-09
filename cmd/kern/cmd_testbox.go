package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/dvorinka/kern/internal/runner"
	"github.com/dvorinka/kern/internal/workflows"
)

// cmdTestbox runs one job of a workflow locally in the docker executor
// (#143): compile the workflow, bundle the working tree (uncommitted
// changes included), and drive the real runner executor against an
// in-process control-plane shim — no server, no fleet, no push.
func (c *cli) cmdTestbox(ctx context.Context, args []string) error {
	opts, pos := parseFlags(args, "job", "dir", "env-file")
	dir, err := filepath.Abs(first(opts["dir"], "."))
	if err != nil {
		return err
	}
	wfArg := ""
	if len(pos) > 0 {
		wfArg = pos[0]
	}
	wfPath, err := testboxWorkflow(dir, wfArg)
	if err != nil {
		return err
	}
	content, err := os.ReadFile(wfPath)
	if err != nil {
		return err
	}
	img := envOr("KERN_DEFAULT_IMAGE", "ubuntu:24.04")
	res, err := workflows.Compile(string(content), workflows.Compiler{
		DefaultImage: img,
		LabelImages:  map[string]string{"ubuntu-latest": img, "ubuntu-24.04": img},
		Resolve:      makeResolver(dir),
	})
	if err != nil {
		return fmt.Errorf("compile %s: %w", wfPath, err)
	}
	if res.Plan == nil || len(res.Plan.Jobs) == 0 {
		return fmt.Errorf("%s compiled to no jobs", wfPath)
	}
	job, err := pickTestboxJob(res.Plan.Jobs, opts["job"])
	if err != nil {
		return err
	}
	bundle, files, _, err := bundleDir(dir)
	if err != nil {
		return fmt.Errorf("bundle: %w", err)
	}
	defer os.Remove(bundle)
	fmt.Fprintf(os.Stderr, "testbox: %d files bundled from %s\n", files, dir)

	secrets := map[string]string{}
	if ef := opts["env-file"]; ef != "" {
		if err := loadEnvFile(ef, secrets); err != nil {
			return err
		}
	}
	workRoot, err := os.MkdirTemp("", "kern-testbox-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workRoot)
	artifactsDir := filepath.Join(dir, ".kern-testbox", "artifacts")

	claim := testboxClaim(res, job, dir)
	logger, _ := zap.NewDevelopment()
	status, err := runner.RunLocalJob(ctx, runner.Config{
		WorkRoot: workRoot,
		Executor: "docker",
	}, claim, secrets, map[string]string{}, logger, os.Stdout, artifactsDir, bundle)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "testbox: job %s → %s\n", job.Key, status)
	if status != "success" {
		os.Exit(1)
	}
	return nil
}

// testboxWorkflow resolves the workflow argument to a file: an explicit
// path, a filename inside .github/workflows, or the directory's only
// workflow when no argument is given.
func testboxWorkflow(dir, arg string) (string, error) {
	if arg != "" {
		if st, err := os.Stat(arg); err == nil && !st.IsDir() {
			return arg, nil
		}
		cand := filepath.Join(dir, ".github", "workflows", arg)
		for _, ext := range []string{"", ".yml", ".yaml"} {
			if st, err := os.Stat(cand + ext); err == nil && !st.IsDir() {
				return cand + ext, nil
			}
		}
		return "", fmt.Errorf("no workflow at %s or .github/workflows/%s", arg, arg)
	}
	entries, _ := filepath.Glob(filepath.Join(dir, ".github", "workflows", "*.y*ml"))
	switch len(entries) {
	case 0:
		return "", fmt.Errorf("no workflows under %s/.github/workflows", dir)
	case 1:
		return entries[0], nil
	default:
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = filepath.Base(e)
		}
		return "", fmt.Errorf("multiple workflows — pass one: %s", strings.Join(names, ", "))
	}
}

func pickTestboxJob(jobs []workflows.JobPlan, key string) (workflows.JobPlan, error) {
	if key != "" {
		for _, j := range jobs {
			if j.Key == key || j.Name == key {
				return j, nil
			}
		}
		return workflows.JobPlan{}, fmt.Errorf("no job %q in plan", key)
	}
	if len(jobs) == 1 {
		return jobs[0], nil
	}
	keys := make([]string, len(jobs))
	for i, j := range jobs {
		keys[i] = j.Key
	}
	return workflows.JobPlan{}, fmt.Errorf("plan has %d jobs — pass --job <%s>", len(jobs), strings.Join(keys, "|"))
}

// testboxClaim turns a compiled JobPlan into a runner claim. The bundle ref
// sends the executor down the /source download path, which the local shim
// serves from the tarball.
func testboxClaim(res workflows.Result, job workflows.JobPlan, dir string) *runner.Claim {
	claim := &runner.Claim{}
	runID, jobID := uuid.NewString(), uuid.NewString()
	claim.Job.ID, claim.Job.RunID = jobID, runID
	claim.Job.Key = job.Key
	claim.Job.Name = first(job.Name, job.Key)
	claim.Job.Attempt = 1
	claim.Job.Image = job.Image
	claim.Job.Env = job.Env
	claim.Job.Matrix = job.Matrix
	claim.Job.RunsOn = job.RunsOn
	claim.Job.TimeoutSeconds = job.TimeoutSeconds
	claim.Job.ContinueOnError = job.ContinueOnError
	claim.Job.OutputsSpec = job.Outputs
	claim.Job.Needs = map[string]any{}
	// ServicePlan and ClaimService share the wire shape; round-trip.
	if len(job.Services) > 0 {
		data, _ := json.Marshal(job.Services)
		_ = json.Unmarshal(data, &claim.Job.Services)
	}
	steps, _ := json.Marshal(job.Steps)
	_ = json.Unmarshal(steps, &claim.Steps)

	claim.Run.ID = runID
	claim.Run.Event = "testbox"
	claim.Run.Ref = "refs/heads/testbox"
	claim.Run.Actor = os.Getenv("USER")
	claim.Run.Workflow = res.Name
	claim.Run.ProjectSlug = "testbox"
	claim.Run.TrustLevel = "trusted"
	claim.Run.BundleRef = "local"
	if sha, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output(); err == nil {
		claim.Run.SHA = strings.TrimSpace(string(sha))
	}
	return claim
}

// loadEnvFile reads KEY=VALUE lines (blank lines and # comments skipped)
// into secrets — the testbox answer to repo secrets. Single quotes do not
// survive; the file is read verbatim.
func loadEnvFile(path string, into map[string]string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || k == "" {
			return fmt.Errorf("%s:%d: not a KEY=VALUE line", path, i+1)
		}
		into[k] = v
	}
	return nil
}
