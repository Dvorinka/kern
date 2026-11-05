package workflows

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/dvorinka/kern/internal/actions"
	"github.com/dvorinka/kern/internal/expressions"
)

var jobIDPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

const (
	maxJobs       = 256
	maxSteps      = 100
	maxMatrixJobs = 256
)

// Compiler resolves a parsed workflow into a plan. ImageFor maps runner labels to an
// execution image; the default applies when nothing matches. Resolve, when set,
// fetches a reusable workflow (jobs.<id>.uses) to YAML; it is the caller's
// responsibility to pin the resolved content to the correct repo and ref.
type Compiler struct {
	DefaultImage string
	LabelImages  map[string]string
	Resolve      func(uses string) (string, error)
}

// Compile parses and compiles a workflow. A non-nil error is reserved for YAML that
// cannot be decoded at all; semantic problems are reported as diagnostics.
func Compile(content string, c Compiler) (Result, error) {
	deps := map[string]string{}
	res, err := compile(content, c, 0, new(int), deps)
	if err == nil && len(deps) > 0 {
		res.Deps = deps
	}
	return res, err
}

func compile(content string, c Compiler, depth int, calls *int, deps map[string]string) (Result, error) {
	d := &diagnostics{}

	var wf Workflow
	dec := yaml.NewDecoder(strings.NewReader(content))
	if err := dec.Decode(&wf); err != nil {
		return Result{
			Valid: false,
			Diagnostics: []Diagnostic{{
				Level: "error", Code: "yaml", Path: "",
				Message: "workflow is not valid YAML: " + err.Error(),
			}},
		}, nil
	}
	detectUnknownTopLevel(content, &wf)
	for key := range wf.Unknown {
		d.errorf("unknown-key", key, "unknown workflow key "+key)
	}

	name := wf.Name
	if name == "" {
		name = "Unnamed workflow"
	}
	res := Result{Workflow: &wf, Name: name, Events: wf.On.Events}

	if len(wf.On.Events) == 0 {
		d.errorf("missing-trigger", "on", "workflow must declare at least one trigger in 'on'")
	}
	if wf.On.Raw != nil {
		allowed := map[string]bool{"branches": true, "branches-ignore": true, "paths": true, "paths-ignore": true,
			"tags": true, "tags-ignore": true, "types": true, "workflows": true,
			"inputs": true, "secrets": true, "schedule": true, "cron": true, "outputs": true}
		for event, cfg := range wf.On.Raw {
			cfgMap, ok := cfg.(map[string]any)
			if !ok {
				continue
			}
			for key := range cfgMap {
				if !allowed[key] {
					d.warnf("trigger-key", "on."+event+"."+key, fmt.Sprintf("trigger filter %q is not implemented by KERN yet and is ignored", key))
				}
			}
		}
	}

	if len(wf.Jobs) == 0 {
		d.errorf("no-jobs", "jobs", "workflow must define at least one job")
	}
	if len(wf.Jobs) > maxJobs {
		d.errorf("too-many-jobs", "jobs", fmt.Sprintf("workflow defines %d jobs; the limit is %d", len(wf.Jobs), maxJobs))
	}

	plan := &Plan{
		Name:             name,
		Events:           wf.On.Events,
		Concurrency:      wf.Concurrency.Group,
		CancelInProgress: wf.Concurrency.CancelInProgress,
		FailFast:         true,
		Triggers:         wf.On.Raw,
	}

	jobIDs := sortedKeys(wf.Jobs)
	// Validate dependency references and cycles before expansion.
	for _, id := range jobIDs {
		job := wf.Jobs[id]
		if job == nil {
			d.errorf("empty-job", "jobs."+id, "job definition is empty")
			continue
		}
		if !jobIDPattern.MatchString(id) {
			d.errorf("job-id", "jobs."+id, "job id must start with a letter or underscore and contain only letters, digits, '-' and '_'")
		}
		for _, need := range job.Needs {
			if _, ok := wf.Jobs[need]; !ok {
				d.errorf("needs-unknown", "jobs."+id+".needs", fmt.Sprintf("job depends on unknown job %q", need))
			}
		}
		if job.Uses != "" {
			if err := ValidateReusableRef(job.Uses); err != nil {
				d.errorf("reusable-workflow", "jobs."+id+".uses", err.Error())
			}
			for k, bad := range map[string]bool{
				"steps": len(job.Steps) > 0, "runs-on": len(job.RunsOn) > 0,
				"env": len(job.Env) > 0, "container": job.Container != nil,
				"services": len(job.Services) > 0, "defaults": job.Defaults != nil,
				"timeout-minutes": job.TimeoutMinutes != 0, "outputs": len(job.Outputs) > 0,
				"permissions": len(job.Permissions) > 0, "environment": job.Environment != nil,
			} {
				if bad {
					d.errorf("reusable-caller", "jobs."+id+"."+k,
						fmt.Sprintf("a reusable-workflow caller may not define %q", k))
				}
			}
			if job.Strategy != nil {
				d.errorf("reusable-caller", "jobs."+id+".strategy",
					"strategy on reusable-workflow callers is not implemented by KERN yet")
			}
			if s, ok := job.Secrets.(string); job.Secrets != nil && !(ok && s == "inherit") {
				d.errorf("reusable-caller", "jobs."+id+".secrets",
					"explicit secrets maps on reusable-workflow callers are not implemented by KERN yet; use 'secrets: inherit'")
			}
			continue
		}
		if len(job.RunsOn) == 0 {
			d.errorf("runs-on", "jobs."+id+".runs-on", "job must declare runs-on")
		}
		if job.Container != nil {
			switch c := job.Container.(type) {
			case string:
				if strings.TrimSpace(c) == "" {
					d.errorf("job-container", "jobs."+id+".container", "container image must not be empty")
				}
			case map[string]any:
				if _, ok := c["image"].(string); !ok {
					d.errorf("job-container", "jobs."+id+".container", "container.image is required")
				}
				for k := range c {
					if k != "image" {
						d.warnf("job-container", "jobs."+id+".container."+k, fmt.Sprintf("container option %q is not implemented and is ignored", k))
					}
				}
			default:
				d.errorf("job-container", "jobs."+id+".container", "container must be an image string or a mapping")
			}
		}
		if len(job.Services) > 0 {
			d.errorf("services", "jobs."+id+".services", "service containers are not implemented by KERN yet")
		}
		if job.Environment != nil {
			switch env := job.Environment.(type) {
			case string:
				if strings.TrimSpace(env) == "" {
					d.errorf("environment", "jobs."+id+".environment", "environment name must not be empty")
				}
			case map[string]any:
				name, ok := env["name"].(string)
				if !ok || strings.TrimSpace(name) == "" {
					d.errorf("environment", "jobs."+id+".environment.name", "environment.name is required and must be a string")
				}
				if u, ok := env["url"]; ok {
					if _, isStr := u.(string); !isStr {
						d.errorf("environment", "jobs."+id+".environment.url", "environment.url must be a string")
					}
				}
				for k := range env {
					if k != "name" && k != "url" {
						d.warnf("environment", "jobs."+id+".environment."+k, fmt.Sprintf("environment option %q is not implemented and is ignored", k))
					}
				}
			default:
				d.errorf("environment", "jobs."+id+".environment", "environment must be a name string or a mapping with name")
			}
		}
		if len(job.Steps) == 0 && job.Uses == "" {
			d.errorf("no-steps", "jobs."+id+".steps", "job must define at least one step")
		}
		if len(job.Steps) > maxSteps {
			d.errorf("too-many-steps", "jobs."+id+".steps", fmt.Sprintf("job defines %d steps; the limit is %d", len(job.Steps), maxSteps))
		}
		seenSteps := map[string]bool{}
		for i := range job.Steps {
			validateStep(d, fmt.Sprintf("jobs.%s.steps[%d]", id, i), job.Steps[i], seenSteps)
		}
		if job.Strategy != nil && len(job.Strategy.Matrix) > 0 {
			d.info("matrix", "jobs."+id+".strategy.matrix", "matrix expansion is limited to 256 combinations")
		}
		if job.Strategy != nil && (job.Strategy.Split.N > 0 || job.Strategy.Split.Auto) {
			if job.Environment != nil {
				d.errorf("split", "jobs."+id+".strategy.split", "strategy.split cannot be combined with environment")
			}
			if _, ok := job.Strategy.Matrix["shard"]; ok {
				d.errorf("split", "jobs."+id+".strategy.split", `strategy.split reserves the matrix axis "shard"`)
			}
		}
	}
	if cyc := detectCycle(wf.Jobs); len(cyc) > 0 {
		d.errorf("cycle", "jobs.needs", "dependency cycle detected: "+strings.Join(cyc, " -> "))
	}
	if len(wf.Permissions) > 0 || hasJobPermissions(wf) {
		d.info("permissions", "permissions", "permissions are recorded but not enforced by KERN; GitHub App scoping still applies")
	}
	if wf.Concurrency.Group == "" && wf.Concurrency.CancelInProgress {
		d.warnf("concurrency", "concurrency.cancel-in-progress", "cancel-in-progress without a group has no effect")
	}
	validateExpressionsIn(d, &wf)

	for _, id := range jobIDs {
		job := wf.Jobs[id]
		if job == nil {
			continue
		}
		if job.Uses != "" {
			if err := ValidateReusableRef(job.Uses); err != nil {
				continue // already diagnosed
			}
			if c.Resolve == nil {
				d.errorf("reusable-resolve", "jobs."+id+".uses",
					"reusable workflow resolution is not configured for this compile")
				continue
			}
			content, err := c.Resolve(job.Uses)
			if err != nil {
				d.errorf("reusable-resolve", "jobs."+id+".uses", err.Error())
				continue
			}
			deps[job.Uses] = content
			compileCall(d, plan, callSpec{
				id: id, name: job.Name, needs: []string(job.Needs),
				ifCond: job.If, with: job.With,
			}, content, c, depth, calls, deps)
			continue
		}
		combos, err := expandMatrix(job)
		if err != nil {
			d.errorf("matrix", "jobs."+id+".strategy.matrix", err.Error())
			continue
		}
		if len(combos) > maxMatrixJobs {
			d.errorf("matrix", "jobs."+id+".strategy.matrix",
				fmt.Sprintf("matrix expands to %d jobs; the limit is %d", len(combos), maxMatrixJobs))
			continue
		}
		for _, combo := range combos {
			jp, err := compileJob(wf, id, job, combo, c)
			if err != nil {
				d.errorf("compile", "jobs."+id, err.Error())
				continue
			}
			plan.Jobs = append(plan.Jobs, jp)
		}
	}

	// Edges: every dependency job id expands to all of its instances.
	byJobID := map[string][]string{}
	for _, jp := range plan.Jobs {
		byJobID[jp.JobID] = append(byJobID[jp.JobID], jp.Key)
	}
	for _, jp := range plan.Jobs {
		for _, need := range jp.Needs {
			for _, from := range byJobID[need] {
				plan.Edges = append(plan.Edges, Edge{From: from, To: jp.Key})
			}
		}
	}

	res.Plan = plan
	res.Diagnostics = d.list
	res.Valid = !d.hasErrors()
	return res, nil
}

func compileJob(wf Workflow, id string, job *Job, matrix map[string]any, c Compiler) (JobPlan, error) {
	ctx := map[string]any{"matrix": matrix}
	displayName := job.Name
	if displayName != "" {
		expanded, err := expressions.Interpolate(displayName, ctx)
		if err != nil {
			return JobPlan{}, err
		}
		displayName = expanded
	}
	key := id
	if len(matrix) > 0 {
		parts := make([]string, 0, len(matrix))
		keys := sortedKeysAny(matrix)
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s: %s", k, asString(matrix[k])))
		}
		suffix := strings.Join(parts, ", ")
		key = fmt.Sprintf("%s (%s)", id, suffix)
		if displayName == "" {
			displayName = key
		}
	}
	if displayName == "" {
		displayName = id
	}

	env := mergeEnv(wf.Env, job.Env)

	// Step defaults come from workflow defaults, overridden per job.
	image := c.resolveImage(job.RunsOn)
	if job.Container != nil {
		switch cont := job.Container.(type) {
		case string:
			image = cont
		case map[string]any:
			if img, ok := cont["image"].(string); ok && img != "" {
				image = img
			}
		}
	}
	shell := wf.Defaults.Run.Shell
	workdir := wf.Defaults.Run.WorkingDirectory
	if job.Defaults != nil {
		if job.Defaults.Run.Shell != "" {
			shell = job.Defaults.Run.Shell
		}
		if job.Defaults.Run.WorkingDirectory != "" {
			workdir = job.Defaults.Run.WorkingDirectory
		}
	}

	timeout := int(job.TimeoutMinutes * 60)
	if job.TimeoutMinutes <= 0 {
		timeout = 3600
	}
	steps := make([]StepPlan, 0, len(job.Steps))
	stepIDs := map[string]int{}
	for i, s := range job.Steps {
		sp := StepPlan{
			Number:           i + 1,
			ID:               stepID(s, i),
			Name:             displayNameOf(s),
			Run:              s.Run,
			Uses:             s.Uses,
			Shell:            firstNonEmpty(s.Shell, shell),
			WorkingDirectory: firstNonEmpty(s.WorkingDirectory, workdir),
			If:               s.If,
			Env:              mergeEnv(nil, s.Env),
			With:             s.With,
			ContinueOnError:  s.ContinueOnError,
		}
		if s.TimeoutMinutes > 0 {
			sp.TimeoutSeconds = int(s.TimeoutMinutes * 60)
		}
		steps = append(steps, sp)
		stepIDs[sp.ID]++
	}

	envName, envURL, err := jobEnvironment(job.Environment, ctx)
	if err != nil {
		return JobPlan{}, err
	}
	splitN, splitAuto := 0, false
	if job.Strategy != nil {
		splitN, splitAuto = job.Strategy.Split.N, job.Strategy.Split.Auto
	}

	return JobPlan{
		Key:             key,
		JobID:           id,
		Name:            displayName,
		Needs:           dedupe(job.Needs),
		RunsOn:          []string(job.RunsOn),
		Image:           image,
		Matrix:          matrix,
		Env:             env,
		If:              job.If,
		Outputs:         job.Outputs,
		TimeoutSeconds:  timeout,
		ContinueOnError: job.ContinueOnError,
		FailFast:        failFast(job),
		Environment:     envName,
		EnvironmentURL:  envURL,
		SplitN:          splitN,
		SplitAuto:       splitAuto,
		Steps:           steps,
	}, nil
}

// jobEnvironment resolves jobs.<id>.environment into a name and optional URL.
// Expressions are interpolated against the matrix context only — GitHub
// evaluates these at runtime with the full job context.
func jobEnvironment(raw any, ctx map[string]any) (string, string, error) {
	var name, url string
	switch env := raw.(type) {
	case nil:
		return "", "", nil
	case string:
		name = env
	case map[string]any:
		name, _ = env["name"].(string)
		url, _ = env["url"].(string)
	}
	var err error
	if name, err = expressions.Interpolate(name, ctx); err != nil {
		return "", "", fmt.Errorf("environment name: %w", err)
	}
	if url != "" {
		if url, err = expressions.Interpolate(url, ctx); err != nil {
			return "", "", fmt.Errorf("environment url: %w", err)
		}
	}
	return name, url, nil
}

func (c Compiler) resolveImage(labels []string) string {
	for _, label := range labels {
		if img, ok := c.LabelImages[label]; ok && img != "" {
			return img
		}
	}
	return c.DefaultImage
}

func displayNameOf(s Step) string {
	if strings.TrimSpace(s.Name) != "" {
		return s.Name
	}
	if s.Uses != "" {
		return "Run " + s.Uses
	}
	firstLine := strings.TrimSpace(strings.SplitN(strings.TrimSpace(s.Run), "\n", 2)[0])
	if len(firstLine) > 80 {
		firstLine = firstLine[:77] + "..."
	}
	if firstLine == "" {
		return "Run"
	}
	return firstLine
}

func mergeEnv(base, overlay map[string]any) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = asString(v)
	}
	for k, v := range overlay {
		out[k] = asString(v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedKeysAny(m map[string]any) []string { return sortedKeys(m) }

func hasJobPermissions(wf Workflow) bool {
	for _, j := range wf.Jobs {
		if j != nil && len(j.Permissions) > 0 {
			return true
		}
	}
	return false
}

// detectUnknownTopLevel flags keys GitHub would reject, so typos do not silently pass.
func detectUnknownTopLevel(content string, wf *Workflow) {
	var raw map[string]yaml.Node
	if err := yaml.Unmarshal([]byte(content), &raw); err != nil {
		return
	}
	known := map[string]bool{"name": true, "run-name": true, "on": true, "env": true, "defaults": true,
		"concurrency": true, "permissions": true, "jobs": true}
	wf.Unknown = map[string]struct{}{}
	for k := range raw {
		if !known[k] {
			wf.Unknown[k] = struct{}{}
		}
	}
}

func validateStep(d *diagnostics, path string, s Step, seenIDs map[string]bool) {
	hasRun := strings.TrimSpace(s.Run) != ""
	hasUses := strings.TrimSpace(s.Uses) != ""
	if !hasRun && !hasUses {
		d.errorf("step-empty", path, "step must define either 'run' or 'uses'")
	}
	if hasRun && hasUses {
		d.errorf("step-both", path, "step cannot define both 'run' and 'uses'")
	}
	if s.ID != "" {
		if !jobIDPattern.MatchString(s.ID) {
			d.errorf("step-id", path+".id", "step id must start with a letter or underscore and contain only letters, digits, '-' and '_'")
		}
		if seenIDs[s.ID] {
			d.errorf("step-id-dup", path+".id", fmt.Sprintf("duplicate step id %q", s.ID))
		}
		seenIDs[s.ID] = true
	}
	if hasUses {
		switch actions.Classify(s.Uses) {
		case actions.KindLocal:
			if err := actions.ValidLocal(s.Uses); err != nil {
				d.errorf("action-local", path+".uses", err.Error())
			}
		case actions.KindRemote:
			if _, err := actions.ParseRemote(s.Uses); err != nil {
				d.errorf("action-ref", path+".uses", err.Error())
			} else {
				d.warnf("action-remote", path+".uses",
					fmt.Sprintf("action %q resolves at run time; KERN does not validate it at compile time", s.Uses))
			}
		case actions.KindInvalid:
			d.errorf("action-uses", path+".uses", fmt.Sprintf("action reference %q is malformed", s.Uses))
		}
		lname := strings.ToLower(strings.SplitN(s.Uses, "@", 2)[0])
		if lname == "actions/upload-artifact" {
			if p, ok := s.With["path"].(string); !ok || strings.TrimSpace(p) == "" {
				d.errorf("artifact-path", path+".with.path", "actions/upload-artifact requires with.path")
			}
		}
		if lname == "actions/cache" || lname == "actions/cache/restore" || lname == "actions/cache/save" {
			if k, ok := s.With["key"].(string); !ok || strings.TrimSpace(k) == "" {
				d.errorf("cache-key", path+".with.key", lname+" requires with.key")
			}
			if p := s.With["path"]; p == nil {
				d.errorf("cache-path", path+".with.path", lname+" requires with.path")
			}
		}
	}
	if s.TimeoutMinutes < 0 {
		d.errorf("step-timeout", path+".timeout-minutes", "timeout-minutes must not be negative")
	}
}

// detectCycle returns one cycle path, or nil when the graph is acyclic.
func detectCycle(jobs map[string]*Job) []string {
	const (
		white = 0
		grey  = 1
		black = 2
	)
	color := map[string]int{}
	var stack []string
	var found []string

	var visit func(string) bool
	visit = func(id string) bool {
		color[id] = grey
		stack = append(stack, id)
		job := jobs[id]
		if job != nil {
			for _, need := range job.Needs {
				if _, ok := jobs[need]; !ok {
					continue
				}
				switch color[need] {
				case grey:
					for i, s := range stack {
						if s == need {
							found = append(append([]string{}, stack[i:]...), need)
							return true
						}
					}
				case white:
					if visit(need) {
						return true
					}
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[id] = black
		return false
	}
	for _, id := range sortedKeys(jobs) {
		if color[id] == white {
			if visit(id) {
				return found
			}
		}
	}
	return nil
}

type diagnostics struct{ list []Diagnostic }

func (d *diagnostics) add(level, code, path, msg string) {
	d.list = append(d.list, Diagnostic{Level: level, Code: code, Path: path, Message: msg})
}

func (d *diagnostics) errorf(code, path, msg string) { d.add("error", code, path, msg) }
func (d *diagnostics) warnf(code, path, msg string)  { d.add("warning", code, path, msg) }
func (d *diagnostics) info(code, path, msg string)   { d.add("info", code, path, msg) }

func (d *diagnostics) hasErrors() bool {
	for _, x := range d.list {
		if x.Level == "error" {
			return true
		}
	}
	return false
}
