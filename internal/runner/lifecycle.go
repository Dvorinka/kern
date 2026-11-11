package runner

// Action lifecycle hooks, per GitHub's wrapper-action ADR (0361).
// `pre`/`pre-entrypoint` run at job setup for every step that declares them,
// in declaration order; `post`/`post-entrypoint` are pushed onto a stack when
// the owning action's pre or main is about to run, then popped LIFO after all
// mains finish. Both gate on pre-if/post-if (default always()). Actions inside
// composites register the same way, but their pre runs inline before main —
// job setup only sees top-level steps. STATE_* values reach post hooks
// through the GITHUB_STATE file command, drained after each phase.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dvorinka/kern/internal/actions"
	"github.com/dvorinka/kern/internal/expressions"
)

// actionHook is a registered post entry. The same pointer is armed by pre and
// main so STATE_* values from every phase accumulate on one hook.
type actionHook struct {
	step       StepSpec
	meta       *actions.Meta
	actionPath string // container-side action dir
	hostDir    string // host action dir (docker image resolution)
	state      map[string]string
}

// hookKey deduplicates registration per step. Composite children reuse the
// parent step number, so the same action nested twice shares one hook —
// GitHub would register two; a documented dedup limit.
func hookKey(step StepSpec) string {
	return fmt.Sprintf("%d|%s", step.Number, step.Uses)
}

func preScript(meta *actions.Meta) string {
	if meta.Runs.Using == "docker" {
		return meta.Runs.PreEntrypoint
	}
	return meta.Runs.Pre
}

func postScript(meta *actions.Meta) string {
	if meta.Runs.Using == "docker" {
		return meta.Runs.PostEntrypoint
	}
	return meta.Runs.Post
}

func preName(meta *actions.Meta) string {
	if meta.Runs.Using == "docker" {
		return "pre-entrypoint"
	}
	return "pre"
}

// runPreHooks is GitHub's job-setup phase: every top-level uses-step's
// declared pre runs before the first main step, in declaration order. Remote
// actions download now — GitHub's "prepare required actions" does the same.
// A failed pre (or resolution) fails the owning step without running main.
func (e *executor) runPreHooks(ctx context.Context) {
	if e.preDone == nil {
		e.preDone = map[string]bool{}
	}
	for _, step := range e.claim.Steps {
		if step.Uses == "" {
			continue
		}
		e.preDone[hookKey(step)] = true
		meta, actionPath, hostDir := e.resolveStepAction(ctx, step)
		if meta == nil {
			continue // resolution already logged + marked the step
		}
		if !e.runPre(ctx, step, meta, actionPath, hostDir) {
			e.markPreFailed(step, "pre hook failed for "+step.Uses)
		}
	}
}

// markPreFailed records that a step's action resolution or pre hook failed;
// runStep then fails the step without running main. The map lazily
// initialises here — writing to a nil map panics the whole agent.
func (e *executor) markPreFailed(step StepSpec, msg string) {
	if e.preFailed == nil {
		e.preFailed = map[int]string{}
	}
	e.preFailed[step.Number] = msg
}

// runNestedPre runs an unregistered action's pre inline before main — only
// composite children reach this; top-level steps were decided at job setup.
func (e *executor) runNestedPre(ctx context.Context, step StepSpec, meta *actions.Meta, actionPath, hostDir string) bool {
	if e.preDone[hookKey(step)] {
		return true
	}
	return e.runPre(ctx, step, meta, actionPath, hostDir)
}

// runPre executes the action's pre entrypoint when declared and pre-if
// passes. The post hook arms here per the ADR: post pushes when pre or main
// is about to run, whichever fires first.
func (e *executor) runPre(ctx context.Context, step StepSpec, meta *actions.Meta, actionPath, hostDir string) bool {
	if preScript(meta) == "" || !e.hookIf(meta.Runs.PreIf, step) {
		return true
	}
	e.logStep(step, "KERN: running "+preName(meta)+" for "+step.Uses)
	h := e.armPostHook(step, meta, actionPath, hostDir)
	if h == nil {
		h = &actionHook{step: step, meta: meta, actionPath: actionPath, hostDir: hostDir, state: map[string]string{}}
	}
	outcome, _ := e.runLifecycleHook(ctx, h, "pre")
	mergeState(h, e.drainState())
	if outcome != "success" {
		e.logStep(step, "KERN: "+preName(meta)+" failed for "+step.Uses)
		return false
	}
	return true
}

// runPostHooks pops registered post hooks LIFO after the last main step —
// GitHub's post-job phase. A hook failure fails the job but never stops the
// remaining hooks: cleanup must run to completion.
func (e *executor) runPostHooks(ctx context.Context) bool {
	ok := true
	for i := len(e.postHooks) - 1; i >= 0; i-- {
		h := e.postHooks[i]
		if !e.hookIf(h.meta.Runs.PostIf, h.step) {
			continue
		}
		e.logStep(h.step, "KERN: running post for "+h.step.Uses)
		if outcome, _ := e.runLifecycleHook(ctx, h, "post"); outcome != "success" {
			e.logStep(h.step, "KERN: post hook failed for "+h.step.Uses)
			ok = false
		}
	}
	return ok
}

// armPostHook registers the action's post hook once. Returns nil when the
// action declares no post — pre and main call it unconditionally and share
// whatever pointer comes back.
func (e *executor) armPostHook(step StepSpec, meta *actions.Meta, actionPath, hostDir string) *actionHook {
	if postScript(meta) == "" {
		return nil
	}
	key := hookKey(step)
	if h, ok := e.postByKey[key]; ok {
		return h
	}
	h := &actionHook{step: step, meta: meta, actionPath: actionPath, hostDir: hostDir, state: map[string]string{}}
	if e.postByKey == nil {
		e.postByKey = map[string]*actionHook{}
	}
	e.postByKey[key] = h
	e.postHooks = append(e.postHooks, h)
	return h
}

// runLifecycleHook executes one pre or post entrypoint for an armed hook.
func (e *executor) runLifecycleHook(ctx context.Context, h *actionHook, phase string) (string, int) {
	var script string
	if phase == "pre" {
		script = preScript(h.meta)
	} else {
		script = postScript(h.meta)
	}
	if h.meta.Runs.Using == "docker" {
		return e.runDockerHook(ctx, h, script)
	}
	return e.runNodeHook(ctx, h, script)
}

// runDockerHook runs a docker action's pre-entrypoint/post-entrypoint as a
// fresh `docker run` of the resolved image — GitHub launches a new container
// per hook, so state only crosses phases through the workspace, HOME or
// STATE_* env. The hook value is the container entrypoint, like runs.entrypoint.
func (e *executor) runDockerHook(ctx context.Context, h *actionHook, script string) (string, int) {
	if script == "" {
		return "success", 0
	}
	if e.host {
		e.logStep(h.step, "KERN: "+h.step.Uses+" (runs.using: docker) needs the docker executor — this runner uses host execution")
		return "failure", 1
	}
	image, err := e.resolveDockerImage(ctx, h.step, h.meta, h.hostDir)
	if err != nil {
		e.logStep(h.step, "KERN: "+err.Error())
		return "failure", 1
	}
	env := e.dockerActionEnv(h.step, h.meta, h.actionPath, h.state)
	return e.dockerActionRun(ctx, h.step, image, script, nil, env)
}

// runNodeHook runs a node action's pre/post script on the bundled runtime.
func (e *executor) runNodeHook(ctx context.Context, h *actionHook, script string) (string, int) {
	if script == "" {
		return "success", 0
	}
	return e.runNodeScript(ctx, h.step, h.meta, h.actionPath, script, h.state)
}

// hookIf evaluates a pre-if/post-if; empty is the ADR's always() default. A
// broken expression warns and runs anyway — a skipped cleanup is worse.
func (e *executor) hookIf(cond string, step StepSpec) bool {
	cond = strings.TrimSpace(cond)
	if cond == "" {
		return true
	}
	ok, err := expressions.EvaluateCondition(cond, e.runnerContext())
	if err != nil {
		e.logStep(step, "KERN: warning: hook condition "+cond+": "+err.Error())
		return true
	}
	return ok
}

// drainState removes the GITHUB_STATE file and returns its name=value pairs —
// @actions/core's saveState appends there and post hooks read them as STATE_*.
func (e *executor) drainState() map[string]string {
	p := filepath.Join(e.cmdDir(), "GITHUB_STATE")
	defer os.Remove(p)
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	kv, _ := parseKeyValue(string(data))
	return kv
}

func mergeState(h *actionHook, kv map[string]string) {
	for k, v := range kv {
		h.state[k] = v
	}
}

// resolveStepAction resolves a top-level uses-step to its action.yml meta,
// container-side dir and host dir. Native and docker:// references carry no
// action.yml. Failures log into the step and mark it failed — the step then
// fails without running main, like GitHub's failed prepare phase.
func (e *executor) resolveStepAction(ctx context.Context, step StepSpec) (*actions.Meta, string, string) {
	ref := strings.TrimSpace(step.Uses)
	var hostDir, actionPath string
	switch actions.Classify(ref) {
	case actions.KindLocal:
		if err := actions.ValidLocal(ref); err != nil {
			e.logStep(step, "KERN: "+err.Error())
			e.markPreFailed(step, err.Error())
			return nil, "", ""
		}
		dir := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(ref)), "./")
		hostDir = filepath.Join(e.workRoot(), "workspace", filepath.FromSlash(dir))
		actionPath = e.wsPath() + "/" + dir
	case actions.KindRemote:
		r, err := actions.ParseRemote(ref)
		if err != nil {
			e.logStep(step, "KERN: "+err.Error())
			e.markPreFailed(step, err.Error())
			return nil, "", ""
		}
		host, container, err := e.fetchAction(ctx, step, r)
		if err != nil {
			e.logStep(step, "KERN: fetch action "+ref+": "+err.Error())
			e.markPreFailed(step, "fetch action: "+err.Error())
			return nil, "", ""
		}
		hostDir, actionPath = host, container
	default:
		return nil, "", ""
	}
	meta, err := readActionMeta(hostDir)
	if err != nil {
		e.logStep(step, "KERN: "+err.Error())
		e.markPreFailed(step, err.Error())
		return nil, "", ""
	}
	return meta, actionPath, hostDir
}
