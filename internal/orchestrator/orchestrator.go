// Package orchestrator advances workflow runs: dependency gating, job conditions,
// fail-fast cancellation and run finalisation. It never executes user code.
package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/dvorinka/kern/internal/expressions"
	"github.com/dvorinka/kern/internal/store"
	"github.com/dvorinka/kern/internal/workflows"
)

type Orchestrator struct {
	Store    *store.Store
	Log      *zap.Logger
	Compiler workflows.Compiler
	// MaxAttempts bounds how often an expired-lease job is retried (KERN_MAX_JOB_ATTEMPTS).
	MaxAttempts int
	// StaleRunnerTimeout flips heartbeats-missed runners offline.
	StaleRunnerTimeout time.Duration
	lastScheduleCheck  time.Time
}

func New(st *store.Store, log *zap.Logger, compiler workflows.Compiler) *Orchestrator {
	return &Orchestrator{
		Store: st, Log: log, Compiler: compiler,
		MaxAttempts: 3, StaleRunnerTimeout: 60 * time.Second,
	}
}

// StartRun compiles the stored workflow version and persists a run plus its jobs.
func (o *Orchestrator) StartRun(ctx context.Context, p store.CreateRunParams) (store.Run, error) {
	version, err := o.Store.WorkflowVersion(ctx, p.WorkflowVersionID)
	if err != nil {
		return store.Run{}, err
	}
	res, err := workflows.Compile(version.Content, o.Compiler)
	if err != nil {
		return store.Run{}, err
	}
	if !res.Valid || res.Plan == nil {
		return store.Run{}, fmt.Errorf("workflow version is invalid")
	}

	triggerCtx := triggerContext(&p)
	if p.ConcurrencyGroup == "" && res.Plan.Concurrency != "" {
		group, err := expressions.Interpolate(res.Plan.Concurrency, triggerCtx)
		if err != nil {
			return store.Run{}, fmt.Errorf("concurrency group: %w", err)
		}
		p.ConcurrencyGroup = group
	}

	p.CancelInProgress = res.Plan.CancelInProgress
	seeds := make([]store.JobSeed, 0, len(res.Plan.Jobs))
	for _, jp := range res.Plan.Jobs {
		seed := store.JobSeed{
			Key:             jp.Key,
			JobKey:          jp.JobID,
			Name:            jp.Name,
			Needs:           jp.Needs,
			RunsOn:          jp.RunsOn,
			Image:           jp.Image,
			Matrix:          jp.Matrix,
			Env:             jp.Env,
			IfCondition:     jp.If,
			TimeoutSeconds:  jp.TimeoutSeconds,
			ContinueOnError: jp.ContinueOnError,
			FailFast:        jp.FailFast,
			OutputsSpec:     jp.Outputs,
		}
		for _, sp := range jp.Steps {
			seed.Steps = append(seed.Steps, store.StepSeed{Key: sp.ID, Name: sp.Name, Spec: sp})
		}
		seeds = append(seeds, seed)
	}
	return o.Store.CreateRun(ctx, p, seeds)
}

// Tick advances every active run. Safe to call concurrently from one process; the
// store operations it uses are idempotent per state transition.
func (o *Orchestrator) Tick(ctx context.Context) {
	if _, err := o.Store.PromoteQueuedRuns(ctx); err != nil {
		o.Log.Error("promote queued runs", zap.Error(err))
	}
	// Reclaim jobs whose runner lost its lease before advancing anything downstream.
	reaped, err := o.Store.ReapExpiredLeases(ctx, o.MaxAttempts)
	if err != nil {
		o.Log.Error("reap expired leases", zap.Error(err))
	} else if len(reaped) > 0 {
		o.Log.Warn("reaped jobs", zap.Int("jobs", len(reaped)))
	}
	if o.StaleRunnerTimeout > 0 {
		if _, err := o.Store.MarkStaleRunnersOffline(ctx, o.StaleRunnerTimeout); err != nil {
			o.Log.Error("mark stale runners", zap.Error(err))
		}
	}
	ids, err := o.Store.ActiveRunIDs(ctx, 200)
	if err != nil {
		o.Log.Error("list active runs", zap.Error(err))
		return
	}
	for _, id := range ids {
		if err := o.AdvanceRun(ctx, id); err != nil {
			o.Log.Error("advance run", zap.String("run_id", id.String()), zap.Error(err))
		}
	}
	o.MintScheduledRuns(ctx, time.Now())
}

// AdvanceRun gates queued jobs on their dependencies and evaluates conditions.
func (o *Orchestrator) AdvanceRun(ctx context.Context, runID uuid.UUID) error {
	run, err := o.Store.Run(ctx, runID)
	if err != nil {
		return err
	}
	if run.Status != "in_progress" {
		return nil
	}
	jobs, err := o.Store.JobsForRun(ctx, runID)
	if err != nil {
		return err
	}
	project, err := o.Store.ProjectByID(ctx, run.ProjectID)
	if err != nil {
		return err
	}

	byJobKey := map[string][]store.Job{}
	for _, j := range jobs {
		byJobKey[j.JobKey] = append(byJobKey[j.JobKey], j)
	}

	for _, job := range jobs {
		if job.Status != "waiting" {
			continue
		}
		status, terminal := dependencyStatus(job.Needs, byJobKey)
		if !terminal {
			continue
		}
		ctxMap := JobContext(job, run, project, byJobKey)
		ctxMap["__job_status"] = status
		ok, evalErr := expressions.EvaluateCondition(job.IfCondition, ctxMap)
		if evalErr != nil {
			_ = o.Store.SetJobSkipped(ctx, job.ID, "if expression error: "+evalErr.Error())
			continue
		}
		if !ok {
			reason := "condition evaluated to false"
			if job.IfCondition != "" {
				reason = "if: " + job.IfCondition + " evaluated to false"
			}
			_ = o.Store.SetJobSkipped(ctx, job.ID, reason)
			continue
		}
		if err := o.Store.SetJobQueued(ctx, job.ID); err != nil {
			return err
		}
	}

	// Fail-fast: a failed matrix member cancels its still-pending siblings.
	for _, job := range jobs {
		if job.Status != "completed" || job.Conclusion != "failure" || !job.FailFast {
			continue
		}
		cancelled, err := o.Store.CancelSiblings(ctx, job.RunID, job.JobKey, job.ID)
		if err != nil {
			return err
		}
		if cancelled > 0 {
			o.Log.Info("fail-fast cancelled sibling jobs",
				zap.String("run_id", runID.String()), zap.String("job", job.Key), zap.Int64("cancelled", cancelled))
		}
	}

	return o.Store.FinishRunIfDone(ctx, runID)
}

// NeedsMap builds the needs.* expression context (result + outputs) from run
// jobs grouped by job_key. Matrix members of the same key are merged; the last
// non-empty conclusion wins, matching JobContext's aggregation.
func NeedsMap(byJobKey map[string][]store.Job) map[string]any {
	needs := map[string]any{}
	for key, jobs := range byJobKey {
		if len(jobs) == 0 {
			continue
		}
		result := "success"
		outputs := map[string]any{}
		for _, j := range jobs {
			if j.Conclusion != "" {
				result = j.Conclusion
			}
			var out map[string]any
			if len(j.Outputs) > 0 && string(j.Outputs) != "{}" {
				_ = json.Unmarshal(j.Outputs, &out)
				for k, v := range out {
					outputs[k] = v
				}
			}
		}
		needs[key] = map[string]any{"result": result, "outputs": outputs}
	}
	return needs
}

// dependencyStatus returns the aggregate status for a job's needs and whether all of
// them have reached a terminal state.
func dependencyStatus(needs []string, byJobKey map[string][]store.Job) (string, bool) {
	status := "success"
	for _, need := range needs {
		jobs, ok := byJobKey[need]
		if !ok || len(jobs) == 0 {
			return "failure", true
		}
		for _, j := range jobs {
			switch j.Status {
			case "completed", "skipped", "cancelled":
			default:
				return "", false
			}
			switch j.Conclusion {
			case "failure", "cancelled":
				status = "failure"
			case "skipped":
				if status == "success" {
					status = "skipped"
				}
			}
		}
	}
	return status, true
}

// JobContext builds the GitHub-compatible expression context for a job.
func JobContext(job store.Job, run store.Run, project store.Project, byJobKey map[string][]store.Job) map[string]any {
	github := githubContext(run, project, job)
	matrix := map[string]any{}
	if len(job.Matrix) > 0 {
		_ = json.Unmarshal(job.Matrix, &matrix)
	}
	needs := NeedsMap(byJobKey)
	env := map[string]any{}
	if raw, err := job.EnvMap(); err == nil {
		for k, v := range raw {
			env[k] = v
		}
	}
	ctx := map[string]any{
		"github":  github,
		"matrix":  matrix,
		"needs":   needs,
		"env":     env,
		"vars":    map[string]any{},
		"job":     map[string]any{"status": jobStatus(job)},
		"runner":  map[string]any{},
		"secrets": map[string]any{},
		"steps":   map[string]any{},
	}
	return ctx
}

func jobStatus(j store.Job) string {
	switch j.Status {
	case "in_progress":
		return "running"
	case "queued", "waiting":
		return "queued"
	case "skipped":
		return "skipped"
	case "cancelled":
		return "cancelled"
	default:
		if j.Conclusion != "" {
			return j.Conclusion
		}
		return "success"
	}
}

// Env builds a resolved environment for the job.
func Env(ctx map[string]any, raw map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		expanded, err := expressions.Interpolate(v, ctx)
		if err != nil {
			return nil, fmt.Errorf("env %s: %w", k, err)
		}
		out[k] = expanded
	}
	return out, nil
}

func githubContext(run store.Run, project store.Project, job store.Job) map[string]any {
	owner, repo := "", project.Slug
	if i := strings.IndexByte(project.RepoFullName, '/'); i > 0 {
		owner, repo = project.RepoFullName[:i], project.RepoFullName[i+1:]
	}
	if owner == "" {
		owner = project.Slug
	}
	refName := strings.TrimPrefix(run.Ref, "refs/heads/")
	ctx := map[string]any{
		"event_name":       run.Event,
		"ref":              run.Ref,
		"ref_name":         refName,
		"ref_type":         "branch",
		"sha":              run.SHA,
		"run_id":           run.ID.String(),
		"run_number":       float64(run.RunNumber),
		"run_attempt":      float64(1),
		"workflow":         run.WorkflowName,
		"job":              job.Key,
		"actor":            run.Actor,
		"repository":       owner + "/" + repo,
		"repository_owner": owner,
		"server_url":       "https://github.com",
		"api_url":          "https://api.github.com",
		"workspace":        "/github/workspace",
		"token":            "",
	}
	if run.Ref == "" {
		ctx["ref"] = "refs/heads/" + project.DefaultBranch
		ctx["ref_name"] = project.DefaultBranch
	}
	return ctx
}

func triggerContext(p *store.CreateRunParams) map[string]any {
	return map[string]any{
		"github": map[string]any{
			"event_name": p.Event,
			"ref":        p.Ref,
			"sha":        p.SHA,
			"actor":      p.Actor,
		},
	}
}
