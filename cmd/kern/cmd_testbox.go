package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	// --var/--secret repeat; parseFlags keeps only the last, so they are
	// pulled out of args first (#376).
	var varPairs, secretPairs []string
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--var" || a == "--secret" {
			if i+1 >= len(args) {
				return fmt.Errorf("%s requires KEY=VALUE", a)
			}
			i++
			if a == "--var" {
				varPairs = append(varPairs, args[i])
			} else {
				secretPairs = append(secretPairs, args[i])
			}
			continue
		}
		if strings.HasPrefix(a, "--var=") {
			varPairs = append(varPairs, strings.TrimPrefix(a, "--var="))
			continue
		}
		if strings.HasPrefix(a, "--secret=") {
			secretPairs = append(secretPairs, strings.TrimPrefix(a, "--secret="))
			continue
		}
		rest = append(rest, a)
	}
	opts, pos := parseFlagsEx(rest,
		[]string{"keep", "dry-run", "privileged", "with-needs"},
		"job", "dir", "env-file", "step", "from-step", "matrix",
		"memory", "cpus", "ref", "event")
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
	job, err := pickTestboxJob(res.Plan.Jobs, opts["job"], opts["matrix"])
	if err != nil {
		return err
	}
	if opts["dry-run"] == "true" {
		return testboxPlan(res, job)
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
	vars := map[string]string{}
	for _, kv := range varPairs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return fmt.Errorf("--var expects KEY=VALUE, got %q", kv)
		}
		vars[k] = v
	}
	for _, kv := range secretPairs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return fmt.Errorf("--secret expects KEY=VALUE, got %q", kv)
		}
		secrets[k] = v
	}
	order := []workflows.JobPlan{job}
	if opts["with-needs"] == "true" {
		var err error
		order, err = testboxDAG(res.Plan.Jobs, job)
		if err != nil {
			return err
		}
		if len(order) > 1 {
			fmt.Fprintf(os.Stderr, "testbox: DAG order %s\n", dagNames(order))
		}
	}
	for _, j := range order {
		if err := execTestboxJob(ctx, res, j, dir, opts, bundle, secrets, vars); err != nil {
			return err
		}
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

func pickTestboxJob(jobs []workflows.JobPlan, key, matrix string) (workflows.JobPlan, error) {
	if matrix != "" {
		var filtered []workflows.JobPlan
		for _, j := range jobs {
			if matrixMatch(j.Matrix, matrix) {
				filtered = append(filtered, j)
			}
		}
		if len(filtered) == 0 {
			return workflows.JobPlan{}, fmt.Errorf("no matrix combo matches %q", matrix)
		}
		jobs = filtered
	}
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

// matrixMatch checks a job's expanded matrix against --matrix k=v[,k2=v2].
func matrixMatch(m map[string]any, sel string) bool {
	for _, kv := range strings.Split(sel, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(kv), "=")
		if !ok || k == "" {
			continue
		}
		got, exists := m[k]
		if !exists || fmt.Sprint(got) != v {
			return false
		}
	}
	return true
}

// testboxPlan renders the compiled job for --dry-run: DAG position,
// services, and the step list — no containers, no bundle (#376).
func testboxPlan(res workflows.Result, job workflows.JobPlan) error {
	fmt.Printf("workflow: %s\n", res.Name)
	fmt.Printf("job:      %s (%s)\n", job.Name, job.Key)
	if len(job.Needs) > 0 {
		fmt.Printf("needs:    %s\n", strings.Join(job.Needs, ", "))
	}
	fmt.Printf("image:    %s\n", first(job.Image, "(default)"))
	if len(job.Matrix) > 0 {
		fmt.Printf("matrix:   %v\n", job.Matrix)
	}
	if len(job.RunsOn) > 0 {
		fmt.Printf("runs-on:  %s\n", strings.Join(job.RunsOn, ", "))
	}
	for _, svc := range job.Services {
		fmt.Printf("service:  %s  ports:%v\n", svc.Image, svc.Ports)
	}
	fmt.Println("steps:")
	for i, s := range job.Steps {
		name := s.Name
		if name == "" {
			name = s.Uses
		}
		if name == "" {
			name = s.Run
			if len(name) > 60 {
				name = name[:60] + "…"
			}
		}
		fmt.Printf("  %2d. %s\n", i+1, name)
	}
	return nil
}

// testboxClaim turns a compiled JobPlan into a runner claim. The bundle ref
// sends the executor down the /source download path, which the local shim
// serves from the tarball. Runs unprivileged by default unless --privileged is set.
func testboxClaim(res workflows.Result, job workflows.JobPlan, dir string, opts map[string]string) *runner.Claim {
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
	claim.Run.Event = first(opts["event"], "push")
	claim.Run.Ref = first(opts["ref"], gitCurrentRef(dir), "refs/heads/main")
	claim.Run.Actor = "testbox"
	claim.Run.Workflow = res.Name
	claim.Run.ProjectSlug = "testbox"
	claim.Run.TrustLevel = "untrusted"
	if opts["privileged"] == "true" {
		claim.Run.TrustLevel = "trusted"
	}
	claim.Run.BundleRef = "local"
	if sha, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output(); err == nil {
		claim.Run.SHA = strings.TrimSpace(string(sha))
	}
	return claim
}

func gitCurrentRef(dir string) string {
	out, err := exec.Command("git", "-C", dir, "symbolic-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// loadEnvFile reads KEY=VALUE lines (blank lines and # comments skipped)
// into secrets — the testbox answer to repo secrets. Supports optional
// leading `export ` and strips matching surrounding single/double quotes.
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
		if strings.HasPrefix(line, "export ") || strings.HasPrefix(line, "export\t") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export"))
		}
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return fmt.Errorf("%s:%d: not a KEY=VALUE line", path, i+1)
		}
		if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
			v = v[1 : len(v)-1]
		}
		into[k] = v
	}
	return nil
}

// execTestboxJob runs one job of a testbox plan — the execution half of
// the entrypoint, factored out so --with-needs can drive a DAG.
func execTestboxJob(ctx context.Context, res workflows.Result, job workflows.JobPlan, dir string, opts map[string]string, bundle string, secrets, vars map[string]string) error {
	var err error
	// --keep pins a stable work root so the toolchain caches under _cache
	// (gomodcache, pnpm, cargo…) survive between iterations — the warm loop
	// that makes testbox sub-second on dependency-heavy steps (#376).
	var workRoot string
	if opts["keep"] == "true" {
		h := sha256.Sum256([]byte(dir + "|" + job.Key))
		home, _ := os.UserHomeDir()
		workRoot = filepath.Join(home, ".cache", "kern", "testbox",
			hex.EncodeToString(h[:6]))
		if err := os.MkdirAll(workRoot, 0o755); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "testbox: keeping work root %s (toolchain caches persist)\n", workRoot)
	} else {
		workRoot, err = os.MkdirTemp("", "kern-testbox-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(workRoot)
	}
	artifactsDir := filepath.Join(dir, ".kern-testbox", "artifacts")

	claim := testboxClaim(res, job, dir, opts)
	// Step-range selection: --step runs only step N; --from-step skips the
	// prefix (a fresh workspace, so setup steps may still be needed — pair
	// with --keep when iterating) (#376).
	if n := atoi(opts["step"]); n > 0 {
		var one []runner.StepSpec
		for _, s := range claim.Steps {
			if s.Number == n {
				one = append(one, s)
			}
		}
		if len(one) == 0 {
			return fmt.Errorf("job has no step %d", n)
		}
		claim.Steps = one
	} else if n := atoi(opts["from-step"]); n > 0 {
		var tail []runner.StepSpec
		for _, s := range claim.Steps {
			if s.Number >= n {
				tail = append(tail, s)
			}
		}
		if len(tail) == 0 {
			return fmt.Errorf("job has no step >= %d", n)
		}
		claim.Steps = tail
	}
	if opts["ref"] != "" {
		claim.Run.Ref = opts["ref"]
	}
	if opts["event"] != "" {
		claim.Run.Event = opts["event"]
	}
	if claim.Job.Env == nil {
		claim.Job.Env = map[string]string{}
	}
	if opts["memory"] != "" {
		claim.Job.Env["KERN_JOB_MEMORY"] = opts["memory"]
	}
	if opts["cpus"] != "" {
		claim.Job.Env["KERN_JOB_CPUS"] = opts["cpus"]
	}
	logger, _ := zap.NewDevelopment()
	status, err := runner.RunLocalJob(ctx, runner.Config{
		WorkRoot: workRoot,
		Executor: "docker",
	}, claim, secrets, vars, logger, os.Stdout, artifactsDir, bundle)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "testbox: job %s → %s\n", job.Key, status)
	if status != "success" {
		return exitCodeError{code: 1}
	}
	return nil
}

// testboxDAG orders the transitive needs closure of target so every job
// runs after its dependencies — Kahn-lite DFS over JobPlan.Needs (#376).
// Returns the ordered slice ending with the target job itself.
func testboxDAG(jobs []workflows.JobPlan, target workflows.JobPlan) ([]workflows.JobPlan, error) {
	byKey := make(map[string]workflows.JobPlan, len(jobs))
	for _, j := range jobs {
		byKey[j.Key] = j
	}
	var order []workflows.JobPlan
	done := map[string]bool{}
	stack := map[string]bool{} // cycle guard — compiler rejects these, belt+braces
	var visit func(key string) error
	visit = func(key string) error {
		if done[key] {
			return nil
		}
		if stack[key] {
			return fmt.Errorf("needs: cycle through job %q", key)
		}
		j, ok := byKey[key]
		if !ok {
			return fmt.Errorf("job %q needs %q which is not in the plan", target.Key, key)
		}
		stack[key] = true
		for _, dep := range j.Needs {
			if err := visit(dep); err != nil {
				return err
			}
		}
		delete(stack, key)
		done[key] = true
		order = append(order, j)
		return nil
	}
	if err := visit(target.Key); err != nil {
		return nil, err
	}
	return order, nil
}

func dagNames(order []workflows.JobPlan) string {
	keys := make([]string, len(order))
	for i, j := range order {
		keys[i] = j.Key
	}
	return strings.Join(keys, " → ")
}
