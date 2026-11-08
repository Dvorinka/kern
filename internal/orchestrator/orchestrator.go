// Package orchestrator advances workflow runs: dependency gating, job conditions,
// fail-fast cancellation and run finalisation. It never executes user code.
package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/dvorinka/kern/internal/blobs"
	"github.com/dvorinka/kern/internal/expressions"
	"github.com/dvorinka/kern/internal/store"
	"github.com/dvorinka/kern/internal/telemetry"
	"github.com/dvorinka/kern/internal/workflows"
)

type Orchestrator struct {
	Store    *store.Store
	Blobs    blobs.Store
	Log      *zap.Logger
	Compiler workflows.Compiler
	// MaxAttempts bounds how often an expired-lease job is retried (KERN_MAX_JOB_ATTEMPTS).
	MaxAttempts int
	// StaleRunnerTimeout flips heartbeats-missed runners offline.
	StaleRunnerTimeout time.Duration
	// CacheMaxAge evicts cache entries untouched for this long (GitHub: 7 days).
	// Zero disables the sweep.
	CacheMaxAge time.Duration
	// LogRetention drops step log and summary blobs of runs completed longer
	// ago than this (KERN_LOG_RETENTION). Zero disables pruning.
	LogRetention time.Duration
	// WebhookRetention drops webhook_deliveries rows older than this
	// (KERN_WEBHOOK_RETENTION). Zero disables pruning.
	WebhookRetention time.Duration
	// AuditRetention drops audit_events rows older than this
	// (KERN_AUDIT_RETENTION). Zero disables pruning.
	AuditRetention time.Duration
	// NotifyRetention drops notify_deliveries rows (sent or dead) older than
	// this (KERN_NOTIFY_RETENTION). Zero disables pruning.
	NotifyRetention    time.Duration
	lastRetentionSweep time.Time
	// PostStatus, when set, reports a run's GitHub commit status. It is called
	// with "pending" at run creation and a terminal state on completion.
	PostStatus func(ctx context.Context, run store.Run, state string)
	// NotifySweep, when set, delivers due outbound notifications. It runs
	// inside the leader-gated tick so exactly one replica sends at a time.
	NotifySweep       func(ctx context.Context)
	lastScheduleCheck time.Time

	leaderMu sync.Mutex
	leader   *store.LeaderSession // held while this replica leads ticks
}

// LeaderLockKey is the Postgres advisory-lock key serializing scheduler
// ticks across api replicas. Distinct from the store test-suite key 8424242.
const LeaderLockKey int64 = 0x4B45524E01 // "KERN" + version byte

// reportStatus invokes PostStatus when configured; reporting failures are
// logged by the callback's owner, not retried here.
func (o *Orchestrator) reportStatus(ctx context.Context, run store.Run, state string) {
	if o.PostStatus != nil {
		o.PostStatus(ctx, run, state)
	}
}

// conclusionState maps a run conclusion to a GitHub commit status state.
func conclusionState(conclusion string) string {
	switch conclusion {
	case "success", "skipped":
		return "success"
	case "failure":
		return "failure"
	default:
		return "error"
	}
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
	// Resolved reusable-workflow contents are pinned on the version at trigger
	// time; recompile against them so the plan is deterministic.
	depContents := map[string]string{}
	if len(version.Deps) > 0 {
		_ = json.Unmarshal(version.Deps, &depContents)
	}
	cc := o.Compiler
	cc.Resolve = func(uses string) (string, error) {
		if c, ok := depContents[uses]; ok {
			return c, nil
		}
		return "", fmt.Errorf("reusable workflow %q was not resolved at trigger time", uses)
	}
	res, err := workflows.Compile(version.Content, cc)
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
		// Dispatch inputs merge over plan (workflow_call) inputs and take part
		// in a second runs-on pass: `${{ inputs.runner }}` only resolves now.
		inputs := make(map[string]string, len(jp.Inputs)+len(p.Inputs))
		for k, v := range jp.Inputs {
			inputs[k] = v
		}
		for k, v := range p.Inputs {
			inputs[k] = fmt.Sprint(v)
		}
		if len(inputs) == 0 {
			inputs = nil
		}
		inputsCtx := make(map[string]any, len(inputs))
		for k, v := range inputs {
			inputsCtx[k] = v
		}
		runsOn := workflows.ResolveRunsOn(jp.RunsOn, map[string]any{"matrix": jp.Matrix, "inputs": inputsCtx})
		// A job with zero labels can never be claimed — it would sit in the
		// queue forever. Fail the seed so the trigger reports an error.
		if len(runsOn) == 0 {
			return store.Run{}, fmt.Errorf("job %q resolved to zero runs-on labels", jp.Key)
		}
		seed := store.JobSeed{
			Key:             jp.Key,
			JobKey:          jp.JobID,
			Name:            jp.Name,
			Needs:           jp.Needs,
			RunsOn:          runsOn,
			Image:           jp.Image,
			Matrix:          jp.Matrix,
			Env:             jp.Env,
			IfCondition:     jp.If,
			TimeoutSeconds:  jp.TimeoutSeconds,
			ContinueOnError: jp.ContinueOnError,
			FailFast:        jp.FailFast,
			OutputsSpec:     jp.Outputs,
			Environment:     jp.Environment,
			EnvironmentURL:  jp.EnvironmentURL,
			SplitN:          jp.SplitN,
			SplitAuto:       jp.SplitAuto,
			Inputs:          inputs,
			Services:        serviceSeeds(jp.Services),
			Permissions:     jp.Permissions,
			NeedsDocker:     jobNeedsDocker(jp.Steps),
		}
		for _, sp := range jp.Steps {
			seed.Steps = append(seed.Steps, store.StepSeed{Key: sp.ID, Name: sp.Name, Spec: sp})
		}
		seeds = append(seeds, seed)
	}
	seeds, err = o.expandRunSplits(ctx, p.ProjectID, seeds)
	if err != nil {
		return store.Run{}, err
	}
	run, err := o.Store.CreateRun(ctx, p, seeds)
	if err != nil {
		return store.Run{}, err
	}
	o.reportStatus(ctx, run, "pending")
	return run, nil
}

// Tick advances every active run on the replica holding scheduler leadership.
// With several api replicas running, a Postgres advisory lock elects exactly
// one leader: the lock is a session-level hold on a dedicated connection, so
// a dead leader's lock dies with its connection and the next contender takes
// over on its next tick. Each tick pings the lock connection — a silently
// dead connection would otherwise let two replicas tick at once.
func (o *Orchestrator) Tick(ctx context.Context) {
	if o.currentLeader(ctx) == nil {
		return
	}
	tickStart := time.Now()
	defer func() { telemetry.ObserveSchedulerTick(time.Since(tickStart).Seconds()) }()

	if _, err := o.Store.PromoteQueuedRuns(ctx); err != nil {
		o.Log.Error("promote queued runs", zap.Error(err))
	}
	if n, err := o.Store.ReleaseDueDeployments(ctx); err != nil {
		o.Log.Error("release due deployments", zap.Error(err))
	} else if n > 0 {
		o.Log.Info("released deployments whose wait elapsed", zap.Int64("jobs", n))
	}
	// Reclaim jobs whose runner lost its lease before advancing anything downstream.
	reaped, err := o.Store.ReapExpiredLeases(ctx, o.MaxAttempts)
	if err != nil {
		o.Log.Error("reap expired leases", zap.Error(err))
	} else if len(reaped) > 0 {
		o.Log.Warn("reaped jobs", zap.Int("jobs", len(reaped)))
	}
	// Cancel queued jobs orphaned under finished runs — they can never be
	// claimed and only inflate the queue count.
	if n, err := o.Store.CancelOrphanedJobs(ctx); err != nil {
		o.Log.Error("cancel orphaned jobs", zap.Error(err))
	} else if n > 0 {
		o.Log.Info("cancelled jobs orphaned by finished runs", zap.Int64("jobs", n))
	}
	// Belt-and-braces job timeout: the runner enforces the same ceiling, so a
	// hit here means a runner renewed its lease without enforcing it.
	if n, err := o.Store.TimeoutOverdueJobs(ctx); err != nil {
		o.Log.Error("timeout overdue jobs", zap.Error(err))
	} else if n > 0 {
		o.Log.Warn("jobs failed by timeout watchdog", zap.Int64("jobs", n))
	}
	if o.StaleRunnerTimeout > 0 {
		if _, err := o.Store.MarkStaleRunnersOffline(ctx, o.StaleRunnerTimeout); err != nil {
			o.Log.Error("mark stale runners", zap.Error(err))
		}
	}
	if o.Blobs != nil {
		if refs, err := o.Store.DeleteExpiredArtifacts(ctx); err != nil {
			o.Log.Error("sweep expired artifacts", zap.Error(err))
		} else {
			for _, ref := range refs {
				_ = o.Blobs.Delete(ref)
			}
		}
		if o.CacheMaxAge > 0 {
			if refs, err := o.Store.DeleteStaleCacheEntries(ctx, o.CacheMaxAge); err != nil {
				o.Log.Error("sweep stale cache", zap.Error(err))
			} else {
				for _, ref := range refs {
					_ = o.Blobs.Delete(ref)
				}
			}
		}
	}
	// One clock for every hourly prune. Log pruning used to stamp the clock
	// and the webhook prune, checked second, never ran while log retention
	// was enabled (the default).
	if time.Since(o.lastRetentionSweep) > time.Hour &&
		(o.LogRetention > 0 || o.WebhookRetention > 0 || o.AuditRetention > 0 || o.NotifyRetention > 0) {
		o.lastRetentionSweep = time.Now()
		if o.Blobs != nil && o.LogRetention > 0 {
			if refs, err := o.Store.PruneStepBlobs(ctx, o.LogRetention); err != nil {
				o.Log.Error("prune step blobs", zap.Error(err))
			} else {
				for _, ref := range refs {
					_ = o.Blobs.Delete(ref)
				}
				if len(refs) > 0 {
					o.Log.Info("log retention pruned step blobs", zap.Int("blobs", len(refs)))
				}
			}
		}
		if o.WebhookRetention > 0 {
			if n, err := o.Store.PruneWebhookDeliveries(ctx, o.WebhookRetention); err != nil {
				o.Log.Error("prune webhook deliveries", zap.Error(err))
			} else if n > 0 {
				o.Log.Info("webhook retention pruned deliveries", zap.Int64("rows", n))
			}
		}
		if o.AuditRetention > 0 {
			if n, err := o.Store.PruneAuditEvents(ctx, o.AuditRetention); err != nil {
				o.Log.Error("prune audit events", zap.Error(err))
			} else if n > 0 {
				o.Log.Info("audit retention pruned events", zap.Int64("rows", n))
			}
		}
		if o.NotifyRetention > 0 {
			if n, err := o.Store.PruneNotifyDeliveries(ctx, o.NotifyRetention); err != nil {
				o.Log.Error("prune notify deliveries", zap.Error(err))
			} else if n > 0 {
				o.Log.Info("notify retention pruned deliveries", zap.Int64("rows", n))
			}
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
	if o.NotifySweep != nil {
		o.NotifySweep(ctx)
	}
}

// currentLeader returns the held leader session, contending for the lock on
// first use and re-verifying the lock connection's liveness every tick.
func (o *Orchestrator) currentLeader(ctx context.Context) *store.LeaderSession {
	o.leaderMu.Lock()
	defer o.leaderMu.Unlock()
	if o.leader != nil {
		if o.leader.Alive(ctx) {
			return o.leader
		}
		o.leader.Release()
		o.leader = nil
		telemetry.SetSchedulerLeader(false)
		o.Log.Warn("scheduler leadership lost: lock connection died")
	}
	ls, ok, err := o.Store.TryLeader(ctx, LeaderLockKey)
	if err != nil {
		o.Log.Error("leader lock", zap.Error(err))
		return nil
	}
	if !ok {
		telemetry.SetSchedulerLeader(false)
		return nil
	}
	o.leader = ls
	telemetry.SetSchedulerLeader(true)
	o.Log.Info("scheduler leadership acquired")
	return ls
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
	jobs, err := o.Store.JobsForRunNoSteps(ctx, runID)
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
		// Environment gate: every environment-named job leaves a deployment
		// record. A protected environment can refuse the ref, then park the
		// job until its approvals are in and its wait has elapsed.
		if job.Environment != "" {
			env, err := o.Store.EnsureEnvironment(ctx, run.ProjectID, job.Environment)
			if err != nil {
				return err
			}
			var waitUntil *time.Time
			if env.Protected && env.WaitMinutes > 0 {
				t := time.Now().Add(time.Duration(env.WaitMinutes) * time.Minute)
				waitUntil = &t
			}
			if _, err := o.Store.CreateDeployment(ctx, run.ProjectID, env.ID, run.ID, job.ID,
				run.Ref, run.SHA, job.EnvironmentURL, waitUntil); err != nil {
				return err
			}
			if env.Protected && !store.BranchAllowed(env.BranchPolicy, env.BranchPatterns, run.Ref) {
				reason := fmt.Sprintf("branch %s is not allowed to deploy to %s", store.RefBranch(run.Ref), env.Name)
				if err := o.Store.FailDeployment(ctx, job.ID, reason); err != nil {
					return err
				}
				continue
			}
			if env.Holds() {
				if err := o.Store.SetJobWaitingApproval(ctx, job.ID); err != nil {
					return err
				}
				continue
			}
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

	if err := o.Store.FinishRunIfDone(ctx, runID); err != nil {
		return err
	}
	// The run may have just reached a terminal state in this call; report the
	// commit status once, on the transition.
	if updated, err := o.Store.Run(ctx, runID); err == nil && updated.Status == "completed" {
		o.reportStatus(ctx, updated, conclusionState(updated.Conclusion))
	}
	return nil
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
				// needs.<id>.result reports the post-continue-on-error
				// conclusion, like GitHub's job result.
				if j.Conclusion == "failure" && j.ContinueOnError {
					result = "success"
				}
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
			case "failure":
				if !j.ContinueOnError {
					status = "failure"
				}
			case "cancelled":
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
	needs := needsWithAliases(job.Key, NeedsMap(byJobKey))
	env := map[string]any{}
	if raw, err := job.EnvMap(); err == nil {
		for k, v := range raw {
			env[k] = v
		}
	}
	inputs := map[string]any{}
	if raw, err := job.InputsMap(); err == nil {
		for k, v := range raw {
			inputs[k] = v
		}
	}
	ctx := map[string]any{
		"github":  github,
		"matrix":  matrix,
		"needs":   needs,
		"env":     env,
		"inputs":  inputs,
		"vars":    map[string]any{},
		"job":     map[string]any{"status": jobStatus(job)},
		"runner":  map[string]any{},
		"secrets": map[string]any{},
		"steps":   map[string]any{},
	}
	return ctx
}

func serviceSeeds(plans []workflows.ServicePlan) []store.ServiceSeed {
	if len(plans) == 0 {
		return nil
	}
	out := make([]store.ServiceSeed, 0, len(plans))
	for _, p := range plans {
		out = append(out, store.ServiceSeed{
			Name: p.Name, Image: p.Image, Env: p.Env, Options: p.Options, Ports: p.Ports,
		})
	}
	return out
}

// dockerCmdRe spots a docker invocation in a run script — word-bounded so
// `docker`, `docker-compose` and `docker compose` all hit while prose like
// "dockers" does not. A false positive only prefers a host runner; harmless.
var dockerCmdRe = regexp.MustCompile(`\bdocker\b`)

// jobNeedsDocker reports whether any step shells out to the docker CLI or
// uses an official docker/* action. Such jobs need a reachable daemon —
// the claim query prefers kern-docker-capable (host-executor) runners.
func jobNeedsDocker(steps []workflows.StepPlan) bool {
	for _, st := range steps {
		if strings.HasPrefix(st.Uses, "docker/") {
			return true
		}
		if dockerCmdRe.MatchString(st.Run) {
			return true
		}
	}
	return false
}

// NeedsWithAliases exposes the alias expansion to the claim endpoint.
func NeedsWithAliases(jobKey string, needs map[string]any) map[string]any {
	return needsWithAliases(jobKey, needs)
}

// needsWithAliases exposes needs entries under the local ids a called
// workflow's expressions use. Expanded jobs carry caller/called key paths and
// prefixed job ids (caller_build); inside the call, `needs.build` must resolve.
func needsWithAliases(jobKey string, needs map[string]any) map[string]any {
	parts := strings.Split(jobKey, "/")
	if len(parts) < 2 {
		return needs
	}
	out := make(map[string]any, len(needs)*2)
	for k, v := range needs {
		out[k] = v
	}
	for k, v := range needs {
		alias := k
		for _, seg := range parts[:len(parts)-1] {
			if !strings.HasPrefix(alias, seg+"_") {
				break
			}
			alias = alias[len(seg)+1:]
			if _, exists := out[alias]; !exists {
				out[alias] = v
			}
		}
	}
	return out
}

func jobStatus(j store.Job) string {
	switch j.Status {
	case "in_progress":
		return "running"
	case "queued":
		return "queued"
	case "waiting", "waiting_approval":
		return "waiting"
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
	refType := "branch"
	if strings.HasPrefix(run.Ref, "refs/tags/") {
		refName = strings.TrimPrefix(run.Ref, "refs/tags/")
		refType = "tag"
	}
	ctx := map[string]any{
		"event_name":       run.Event,
		"ref":              run.Ref,
		"ref_name":         refName,
		"ref_type":         refType,
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
	// github.head_ref is the PR head branch for pull_request deliveries —
	// the documented way pull_request_target jobs filter on the PR branch.
	var src map[string]any
	if json.Unmarshal(run.Source, &src) == nil {
		if ref, ok := src["head_ref"].(string); ok && ref != "" {
			ctx["head_ref"] = ref
		}
		if sha, ok := src["head_sha"].(string); ok && sha != "" {
			ctx["head_sha"] = sha
		}
		ctx["event"] = workflows.EventPayload(run.Event, run.Ref, run.SHA, run.Actor,
			run.CommitMessage, project.RepoFullName, project.RepoURL, project.DefaultBranch, src)
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
