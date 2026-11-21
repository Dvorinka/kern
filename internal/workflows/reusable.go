package workflows

// Reusable-workflow expansion (jobs.<id>.uses). The referenced workflow is
// resolved to YAML by the caller's Resolve func, compiled recursively, and
// its jobs are folded into the caller's plan with prefixed job ids
// (caller_called). A synthetic boundary job named after the caller id parks
// on the called leaf jobs and carries the caller's if plus the
// workflow_call outputs mapping, so `needs.<caller>` and
// `needs.<caller>.outputs` resolve the way GitHub presents them.

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	maxCallDepth = 4
	maxCalls     = 20
)

var remoteReusableRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/[\w./-]+\.ya?ml@[\w./-]+$`)

// ValidateReusableRef checks the shape of a jobs.<id>.uses reference.
func ValidateReusableRef(uses string) error {
	u := strings.TrimSpace(uses)
	if strings.HasPrefix(u, "./") {
		if strings.Contains(u, "@") {
			return fmt.Errorf("local reusable workflow %q cannot pin a ref; it always uses the run's ref", u)
		}
		if !strings.HasSuffix(u, ".yml") && !strings.HasSuffix(u, ".yaml") {
			return fmt.Errorf("reusable workflow %q must reference a .yml/.yaml file", u)
		}
		if strings.Contains(u, "..") {
			return fmt.Errorf("reusable workflow %q escapes the repository root", u)
		}
		if !strings.HasPrefix(u, "./.github/workflows/") {
			return fmt.Errorf("reusable workflow %q must live under .github/workflows/", u)
		}
		return nil
	}
	if !remoteReusableRe.MatchString(u) {
		return fmt.Errorf("reusable workflow %q must be ./.github/workflows/x.yml or owner/repo/.github/workflows/x.yml@ref", u)
	}
	return nil
}

// callSpec carries the parsed caller side of a uses job.
type callSpec struct {
	id      string
	name    string
	needs   []string
	ifCond  string
	with    map[string]any
	secrets any
}

// compileCall expands one jobs.<id>.uses caller into the plan. content is the
// resolved YAML of the called workflow.
func compileCall(d *diagnostics, plan *Plan, spec callSpec, content string, c Compiler, depth int, calls *int, deps map[string]string) {
	*calls++
	if *calls > maxCalls {
		d.errorf("reusable-call-cap", "jobs."+spec.id+".uses",
			fmt.Sprintf("more than %d reusable workflow calls in one run", maxCalls))
		return
	}
	if depth+1 > maxCallDepth {
		d.errorf("reusable-depth", "jobs."+spec.id+".uses",
			fmt.Sprintf("reusable workflow chains deeper than %d are not supported", maxCallDepth))
		return
	}
	res, err := compile(content, c, depth+1, calls, deps)
	if err != nil {
		d.errorf("reusable-compile", "jobs."+spec.id+".uses", "called workflow is not valid YAML: "+err.Error())
		return
	}
	if res.Workflow == nil {
		d.errorf("reusable-compile", "jobs."+spec.id+".uses", "called workflow did not parse")
		return
	}
	called := res.Workflow
	if !hasEvent(called, "workflow_call") {
		d.errorf("reusable-trigger", "jobs."+spec.id+".uses",
			"called workflow must declare 'on: workflow_call'")
		return
	}
	for _, x := range res.Diagnostics {
		if x.Level == "error" {
			d.errorf("reusable-compile", "jobs."+spec.id+".uses", "called workflow is invalid: "+x.Message)
			return
		}
	}
	inputs, ierr := resolveCallInputs(workflowCallInputs(called), spec.with)
	if ierr != nil {
		d.errorf("reusable-inputs", "jobs."+spec.id+".with", ierr.Error())
		return
	}
	secretsMap, serr := callSecretsMap(spec.secrets)
	if serr != nil {
		d.errorf("reusable-secrets", "jobs."+spec.id+".secrets", serr.Error())
		return
	}
	expandCallInto(plan, res.Plan, called, spec, inputs, secretsMap, c)
}

// callSecretsMap normalizes a caller's jobs.<id>.secrets into the per-job
// delivery spec stamped on expanded jobs: 'inherit' yields nil (the full
// eligible set), an omitted secrets key yields an empty map (none — GitHub
// passes no secrets unless asked), and a map yields called-name ->
// caller-side expression or literal.
func callSecretsMap(raw any) (map[string]string, error) {
	if raw == nil {
		return map[string]string{}, nil
	}
	if s, ok := raw.(string); ok {
		if s == "inherit" {
			return nil, nil
		}
		return nil, fmt.Errorf("jobs.secrets must be 'inherit' or a map of secret names to expressions, got %q", s)
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("jobs.secrets must be 'inherit' or a map of secret names to expressions")
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = asString(v)
	}
	return out, nil
}

func hasEvent(wf *Workflow, name string) bool {
	for _, e := range wf.On.Events {
		if e == name {
			return true
		}
	}
	return false
}

type callInput struct {
	Required   bool
	Default    string
	HasDefault bool
}

// workflowCallInputs reads the on.workflow_call.inputs declarations.
func workflowCallInputs(wf *Workflow) map[string]callInput {
	out := map[string]callInput{}
	cfg, _ := wf.On.Raw["workflow_call"].(map[string]any)
	ins, _ := cfg["inputs"].(map[string]any)
	for name, v := range ins {
		m, _ := v.(map[string]any)
		in := callInput{}
		if req, ok := m["required"].(bool); ok {
			in.Required = req
		}
		if def, ok := m["default"]; ok {
			in.Default, in.HasDefault = asString(def), true
		}
		out[name] = in
	}
	return out
}

// resolveCallInputs merges declared inputs, defaults and caller `with:` —
// the same contract as composite action inputs.
func resolveCallInputs(decls map[string]callInput, with map[string]any) (map[string]string, error) {
	inputs := map[string]string{}
	for name, in := range decls {
		v, found := in.Default, in.HasDefault
		for k, w := range with {
			if strings.EqualFold(k, name) {
				v, found = asString(w), true
			}
		}
		if !found && in.Required {
			return nil, fmt.Errorf("required input %q was not supplied", name)
		}
		inputs[name] = v
	}
	for k, w := range with {
		if _, ok := inputs[k]; !ok {
			inputs[k] = asString(w)
		}
	}
	return inputs, nil
}

// composeSecretsMap combines a parent caller's secrets spec with a nested
// called job's secrets spec so nested calls can only narrow, never widen,
// secret delivery (e.g. A --inherit--> B --none--> C delivers nothing to C).
func composeSecretsMap(parent, child map[string]string) map[string]string {
	if parent == nil {
		// Parent inherited all secrets; keep child's restriction (nil or map).
		return child
	}
	if len(parent) == 0 {
		// Parent passed no secrets; child cannot widen beyond empty.
		return map[string]string{}
	}
	if child == nil {
		// Child inherited parent's secrets map.
		return parent
	}
	if len(child) == 0 {
		// Child explicitly passed no secrets.
		return map[string]string{}
	}
	// Both parent and child specified explicit maps: keep child entries whose
	// value references a secret passed by parent (or literal), remapping
	// ${{ secrets.X }} to parent[X] when present.
	out := make(map[string]string, len(child))
	for k, expr := range child {
		trimmed := strings.TrimSpace(expr)
		if strings.HasPrefix(trimmed, "${{") && strings.HasSuffix(trimmed, "}}") {
			inner := strings.TrimSpace(trimmed[3 : len(trimmed)-2])
			if strings.HasPrefix(inner, "secrets.") {
				refName := strings.TrimPrefix(inner, "secrets.")
				if pExpr, ok := parent[refName]; ok {
					out[k] = pExpr
				}
				continue
			}
		}
		out[k] = expr
	}
	return out
}

// expandCallInto folds a compiled called-workflow plan into the caller plan.
// secretsMap is composed with deeper call specs so nested calls can never
// widen secret delivery or overwrite a deeper call's resolved inputs.
func expandCallInto(plan *Plan, called *Plan, calledWf *Workflow, spec callSpec, inputs map[string]string, secretsMap map[string]string, c Compiler) {
	prefix := spec.id + "_"
	for _, jp := range called.Jobs {
		needs := make([]string, 0, len(jp.Needs)+len(spec.needs))
		for _, n := range jp.Needs {
			needs = append(needs, prefix+n)
		}
		if len(jp.Needs) == 0 {
			// Caller needs fan into the called entry jobs.
			needs = append(needs, spec.needs...)
		}
		nj := jp
		nj.JobID = prefix + jp.JobID
		nj.Key = spec.id + "/" + jp.Key
		nj.Name = spec.id + "/" + jp.Name
		nj.Needs = needs
		nj.If = combineIf(spec.ifCond, jp.If)
		if len(nj.Inputs) == 0 {
			nj.Inputs = inputs
		}
		nj.SecretsMap = composeSecretsMap(secretsMap, jp.SecretsMap)
		plan.Jobs = append(plan.Jobs, nj)
	}
	// The boundary waits on every called job so needs.<called>.outputs is
	// resolvable in its outputs mapping and the call appears as one node.
	boundaryNeeds := make([]string, 0, len(called.Jobs))
	for _, j := range called.Jobs {
		boundaryNeeds = append(boundaryNeeds, prefix+j.JobID)
	}
	outputs := map[string]string{}
	for name, tmpl := range workflowCallOutputs(calledWf) {
		outputs[name] = jobsRefRe.ReplaceAllString(tmpl, "needs."+prefix+"$1.outputs")
	}
	name := spec.name
	if name == "" {
		name = spec.id
	}
	plan.Jobs = append(plan.Jobs, JobPlan{
		Key:            spec.id,
		JobID:          spec.id,
		Name:           name,
		Needs:          boundaryNeeds,
		RunsOn:         []string{"self-hosted"},
		Image:          c.DefaultImage,
		If:             spec.ifCond,
		Outputs:        outputs,
		TimeoutSeconds: 600,
		FailFast:       true,
		// The boundary runs a trivial step; it never needs secrets.
		SecretsMap: map[string]string{},
		Steps: []StepPlan{{
			Number: 1, ID: "__boundary", Name: "reusable workflow boundary", Run: "true",
		}},
	})
	// Edges are derived from Needs by the caller after expansion; nothing
	// extra is required here.
}

var jobsRefRe = regexp.MustCompile(`jobs\.([A-Za-z_][A-Za-z0-9_-]*)\.outputs`)

// combineIf joins a caller's if with a called job's if. The caller condition
// must gate the whole subgraph — a false caller if skips every called job,
// including always() jobs, exactly as the call would be skipped on GitHub.
func combineIf(caller, job string) string {
	strip := func(s string) string {
		s = strings.TrimSpace(s)
		if strings.HasPrefix(s, "${{") && strings.HasSuffix(s, "}}") {
			s = strings.TrimSpace(s[3 : len(s)-2])
		}
		return s
	}
	caller, job = strip(caller), strip(job)
	switch {
	case caller == "":
		return job
	case job == "":
		return caller
	default:
		return "(" + caller + ") && (" + job + ")"
	}
}

// workflowCallOutputs reads on.workflow_call.outputs declarations.
func workflowCallOutputs(wf *Workflow) map[string]string {
	out := map[string]string{}
	cfg, _ := wf.On.Raw["workflow_call"].(map[string]any)
	outs, _ := cfg["outputs"].(map[string]any)
	for name, v := range outs {
		m, _ := v.(map[string]any)
		if val, ok := m["value"].(string); ok {
			out[name] = val
		}
	}
	return out
}
