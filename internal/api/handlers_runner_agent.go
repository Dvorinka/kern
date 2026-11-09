package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/dvorinka/kern/internal/auth"
	"github.com/dvorinka/kern/internal/blobs"
	"github.com/dvorinka/kern/internal/expressions"
	"github.com/dvorinka/kern/internal/orchestrator"
	"github.com/dvorinka/kern/internal/secrets"
	"github.com/dvorinka/kern/internal/store"
	"github.com/dvorinka/kern/internal/workflows"
)

type registerRequest struct {
	JoinToken   string   `json:"join_token"`
	Name        string   `json:"name"`
	Hostname    string   `json:"hostname"`
	OS          string   `json:"os"`
	Arch        string   `json:"arch"`
	CPUs        int      `json:"cpus"`
	MemoryBytes int64    `json:"memory_bytes"`
	DiskBytes   int64    `json:"disk_bytes"`
	Labels      []string `json:"labels"`
	Version     string   `json:"version"`
	Ephemeral   bool     `json:"ephemeral"`
	Executor    string   `json:"executor"`
}

// registerRunner accepts either the shared static join token (dev escape
// hatch) or a one-time enrollment token minted via POST /enroll-tokens.
func (s *Server) registerRunner(c *gin.Context) {
	var req registerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_body", "invalid registration body")
		return
	}
	var orgID uuid.UUID
	var enrollCfg store.RunnerEnrollConfig
	if s.joinTokenValid(req.JoinToken) {
		org, err := s.Store.FirstOrg(c.Request.Context())
		if err != nil {
			s.internalError(c, err)
			return
		}
		orgID = org.ID
	} else {
		var err error
		orgID, enrollCfg, err = s.Store.ConsumeEnrollToken(c.Request.Context(), auth.HashToken(req.JoinToken))
		if err != nil {
			fail(c, http.StatusUnauthorized, "invalid_join_token",
				"join token is invalid, expired or already used")
			return
		}
		s.Store.Audit(c.Request.Context(), orgID, nil, "runner.enroll", req.Name)
	}
	if req.Name == "" {
		req.Name = "runner-" + uuid.NewString()[:8]
	}
	switch req.Executor {
	case "", "docker", "host":
	default:
		fail(c, http.StatusBadRequest, "invalid_executor", "executor must be docker or host")
		return
	}
	token, hash, err := auth.NewToken(auth.RunnerPrefix)
	if err != nil {
		s.internalError(c, err)
		return
	}
	runner, err := s.Store.RegisterRunner(c.Request.Context(), store.RegisterRunnerParams{
		OrgID: orgID, Name: req.Name, Hostname: req.Hostname, OS: req.OS, Arch: req.Arch,
		CPUs: req.CPUs, MemoryBytes: req.MemoryBytes, DiskBytes: req.DiskBytes,
		Labels: req.Labels, Version: req.Version, TokenHash: hash, Ephemeral: req.Ephemeral,
		Executor: req.Executor,
	})
	if err != nil {
		s.internalError(c, err)
		return
	}
	s.Log.Info("runner registered", zap.String("runner", runner.Name), zap.String("runner_id", runner.ID.String()))
	if ip := clientIP(c); geoForRunner(runner, ip) {
		go s.resolveRunnerGeo(runner.ID, ip)
	}
	// Enrollment-time desired config rides the token: auto_update opt-in and
	// operator env overrides land on the runner row at registration.
	if enrollCfg.AutoUpdate != nil {
		_ = s.Store.SetRunnerAutoUpdate(c.Request.Context(), orgID, runner.ID, *enrollCfg.AutoUpdate)
	}
	if enrollCfg.Env != nil {
		_ = s.Store.SetRunnerEnv(c.Request.Context(), orgID, runner.ID, enrollCfg.Env)
	}
	c.JSON(http.StatusCreated, gin.H{"runner_id": runner.ID, "name": runner.Name, "token": token})
}

func (s *Server) runnerHeartbeat(c *gin.Context) {
	runner, _ := currentRunner(c)
	var body struct {
		store.HostMetrics
		ActiveJobs int  `json:"active_jobs"`
		SelfUpdate bool `json:"self_update"`
		// Deregister asks the control plane to delete this runner's row —
		// the ephemeral-runner exit path. Refused while jobs are in flight.
		Deregister bool `json:"deregister"`
	}
	// An empty body is legal (older runners); fields stay nil.
	if err := c.ShouldBindJSON(&body); err != nil && err != io.EOF {
		fail(c, http.StatusBadRequest, "invalid_body", "invalid heartbeat body")
		return
	}
	if err := s.Store.Heartbeat(c.Request.Context(), runner.ID, body.HostMetrics); err != nil {
		s.internalError(c, err)
		return
	}
	if ip := clientIP(c); geoForRunner(runner, ip) {
		go s.resolveRunnerGeo(runner.ID, ip)
	}
	resp := gin.H{"ok": true, "draining": runner.Draining}
	jobs, err := s.Store.RunnerActiveJobs(c.Request.Context(), runner.ID)
	if err != nil {
		c.JSON(http.StatusOK, resp)
		return
	}
	if body.Deregister {
		if len(jobs) == 0 {
			if err := s.Store.DeleteRunner(c.Request.Context(), runner.OrgID, runner.ID); err == nil {
				s.Store.Audit(c.Request.Context(), runner.OrgID, nil, "runner.deregister", runner.Name)
				s.Log.Info("ephemeral runner deregistered", zap.String("runner", runner.Name))
				c.JSON(http.StatusOK, gin.H{"ok": true, "deregistered": true})
				return
			}
		}
		resp["deregistered"] = false
	}
	active := len(jobs)
	if body.ActiveJobs > active {
		active = body.ActiveJobs
	}
	if image, ver, ok := s.offerRunnerUpdate(c.Request.Context(), runner, body.SelfUpdate || runner.AutoUpdate, active); ok {
		resp["update"] = gin.H{"version": ver, "image": image}
	}
	// Operator-managed env overrides ride every heartbeat; the agent merges
	// them into each job's base env (app values win over container defaults).
	if len(runner.Env) > 0 {
		resp["env"] = runner.Env
	}
	c.JSON(http.StatusOK, resp)
}

// claimJob long-polls for compatible work; it returns 204 when the wait expires.
func (s *Server) claimJob(c *gin.Context) {
	runner, _ := currentRunner(c)
	wait := parseInt(c.DefaultQuery("wait", "25"), 25)
	if wait < 0 {
		wait = 0
	}
	if wait > 30 {
		wait = 30
	}
	deadline := time.Now().Add(time.Duration(wait) * time.Second)
	for {
		claimStart := time.Now()
		claimed, err := s.Store.ClaimJob(c.Request.Context(), runner.ID, s.Cfg.JobLease)
		if s.metrics != nil {
			s.metrics.observeClaimQuery(time.Since(claimStart))
		}
		if err != nil {
			s.internalError(c, err)
			return
		}
		if claimed != nil {
			s.respondWithClaim(c, claimed, runner)
			return
		}
		if time.Now().After(deadline) {
			c.Status(http.StatusNoContent)
			return
		}
		select {
		case <-c.Request.Context().Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// jobImageFor picks the image the claiming runner should pull. The default
// job image gets an arch-specific substitute on arm64 runners when one is
// configured; explicit container: and label-mapped images are untouched.
func (s *Server) jobImageFor(runner store.Runner, image string) string {
	if runner.Arch == "arm64" && s.Cfg.Arm64Image != "" && image == s.Cfg.DefaultImage {
		return s.Cfg.Arm64Image
	}
	return image
}

func (s *Server) respondWithClaim(c *gin.Context, claimed *store.ClaimedJob, runner store.Runner) {
	steps := make([]gin.H, 0, len(claimed.Job.Steps))
	for _, st := range claimed.Job.Steps {
		var spec workflows.StepPlan
		if len(st.Spec) > 0 {
			_ = json.Unmarshal(st.Spec, &spec)
		}
		steps = append(steps, gin.H{
			"number": st.Number, "id": spec.ID, "name": st.Name, "status": st.Status,
			"run": spec.Run, "uses": spec.Uses, "shell": spec.Shell,
			"working_directory": spec.WorkingDirectory, "if": spec.If,
			"env": spec.Env, "with": spec.With, "continue_on_error": spec.ContinueOnError,
			"timeout_seconds": spec.TimeoutSeconds,
		})
	}
	secretNames := []string{}
	if project, err := s.Store.ProjectByID(c.Request.Context(), claimed.Run.ProjectID); err == nil {
		if secretRows, err := s.Store.SecretsForProject(c.Request.Context(), project.OrgID, project.ID); err == nil {
			for _, sec := range secretRows {
				secretNames = append(secretNames, sec.Name)
			}
		}
		if claimed.Job.Environment != "" {
			if envRows, err := s.Store.SecretsForEnvironment(c.Request.Context(), project.ID, claimed.Job.Environment); err == nil {
				for _, sec := range envRows {
					secretNames = append(secretNames, sec.Name)
				}
			}
		}
	}
	// needs.* context: the runner resolves needs.<id>.outputs in step bodies and
	// conditions; by then every needed job has reached a terminal state.
	needsCtx := map[string]any{}
	if jobs, err := s.Store.JobsForRunNoSteps(c.Request.Context(), claimed.Job.RunID); err == nil {
		byJobKey := map[string][]store.Job{}
		for _, j := range jobs {
			byJobKey[j.JobKey] = append(byJobKey[j.JobKey], j)
		}
		all := orchestrator.NeedsMap(byJobKey)
		for _, need := range claimed.Job.Needs {
			if v, ok := all[need]; ok {
				needsCtx[need] = v
			}
		}
		// Inside a reusable-workflow subgraph the same entries are visible
		// under the called workflow's local job ids.
		needsCtx = orchestrator.NeedsWithAliases(claimed.Job.Key, needsCtx)
	}
	remediations, _ := s.Store.ApprovedFixes(c.Request.Context(), claimed.Job.ID)
	policy, _ := s.Store.SelfHealPolicy(c.Request.Context(), claimed.Run.ID)
	// Private repos clone with a short-lived installation token minted at claim
	// time. It rides the claim payload only — never the stored run source.
	var cloneToken string
	if s.GH.App != nil {
		if full, ok := claimed.Source["repo_full_name"].(string); ok && full != "" {
			if tok, err := s.GH.App.CloneToken(c.Request.Context(), full); err == nil {
				cloneToken = tok
			} else {
				s.Log.Warn("clone token mint failed; public clones unaffected",
					zap.String("repo", full), zap.Error(err))
			}
		}
	}
	// GITHUB_TOKEN: a per-run installation token scoped to this repository and
	// narrowed by the workflow's permissions: ceiling. Untrusted runs still
	// get a token — GitHub parity gives fork PRs a read-only GITHUB_TOKEN so
	// API-backed actions (paths-filter, github-script) work — but the ceiling
	// is forced read-only and the workflow's permissions: block is ignored.
	// Repository secrets remain eligible-trust only.
	var githubToken string
	if s.GH.App != nil {
		if full, ok := claimed.Source["repo_full_name"].(string); ok && full != "" {
			var perms map[string]string
			if secretsEligibleTrust(claimed.Run.TrustLevel) {
				if len(claimed.Job.Permissions) > 0 && string(claimed.Job.Permissions) != "null" {
					_ = json.Unmarshal(claimed.Job.Permissions, &perms)
				}
			} else {
				perms = forkTokenPermissions
			}
			if tok, _, err := s.GH.App.RunToken(c.Request.Context(), full, perms); err == nil {
				githubToken = tok
			} else {
				s.Log.Warn("github token mint failed; steps run without GITHUB_TOKEN",
					zap.String("repo", full), zap.String("job", claimed.Job.Key), zap.Error(err))
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"job": gin.H{
			"id": claimed.Job.ID, "run_id": claimed.Job.RunID, "key": claimed.Job.Key,
			// Job-scoped cache credential: steps receive it as
			// ACTIONS_RUNTIME_TOKEN so @actions/cache works without ever
			// exposing the runner's broad token to job code.
			"cache_token": s.cacheJobToken(claimed.Job.ID),
			"name":        claimed.Job.Name, "attempt": claimed.Attempt, "image": s.jobImageFor(runner, claimed.Job.Image),
			"env": claimed.Job.Env, "matrix": claimed.Job.Matrix, "runs_on": claimed.Job.RunsOn,
			"timeout_seconds": claimed.Job.TimeoutSeconds, "continue_on_error": claimed.Job.ContinueOnError,
			"outputs_spec": claimed.Job.OutputsSpec,
			"inputs":       claimed.Job.Inputs,
			"services":     claimed.Job.Services,
			"needs":        needsCtx,
		},
		"run": gin.H{
			"id": claimed.Run.ID, "number": claimed.Run.RunNumber, "event": claimed.Run.Event,
			"ref": claimed.Run.Ref, "sha": claimed.Run.SHA, "actor": claimed.Run.Actor,
			"workflow": claimed.Run.WorkflowName, "project": claimed.Run.ProjectName,
			"project_slug": claimed.Run.ProjectSlug, "trust_level": claimed.Run.TrustLevel,
			"repo_url": claimed.Source["repo_url"], "repo_full_name": claimed.Source["repo_full_name"],
			"clone_token":    cloneToken,
			"github_token":   githubToken,
			"default_branch": claimed.Source["default_branch"], "local_path": claimed.Source["local_path"],
			"bundle_ref": claimed.Source["bundle_ref"], "head_ref": claimed.Source["head_ref"],
			"head_sha": claimed.Source["head_sha"],
			// The full source map feeds the GITHUB_EVENT_PATH payload (PR
			// numbers, before SHAs, base refs) that third-party actions read.
			"source":         claimed.Source,
			"commit_message": claimed.Run.CommitMessage,
		},
		"steps":            steps,
		"secrets":          secretNames,
		"lease_expires_at": claimed.LeaseExpiresAt,
		"remediations":     remediations,
		"self_heal":        policy,
	})
}

func (s *Server) renewJobLease(c *gin.Context) {
	job, runner, ok := s.runnerJob(c)
	if !ok {
		return
	}
	alive, err := s.Store.RenewLease(c.Request.Context(), runner.ID, job.ID, s.Cfg.JobLease)
	if err != nil {
		s.internalError(c, err)
		return
	}
	runStatus, _, _ := s.Store.RunStatus(c.Request.Context(), job.RunID)
	c.JSON(http.StatusOK, gin.H{"ok": alive, "cancelled": !alive || runStatus == "cancelled"})
}

func (s *Server) startStep(c *gin.Context) {
	job, _, ok := s.runnerJob(c)
	if !ok {
		return
	}
	n := parseInt(c.Param("n"), 0)
	step, err := s.Store.StartStep(c.Request.Context(), job.ID, n)
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"step": step})
}

// appendLog stores a masked log chunk for a step.
func (s *Server) appendLog(c *gin.Context) {
	job, _, ok := s.runnerJob(c)
	if !ok {
		return
	}
	n := parseInt(c.Param("n"), 0)
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_body", "log chunk too large or unreadable")
		return
	}
	masker := s.maskerForJob(c, job)
	data := masker.MaskBytes(body)
	key := blobs.LogKey(job.RunID.String(), job.ID.String(), n)
	size, err := s.Blobs.Append(key, data)
	if err != nil {
		s.internalError(c, err)
		return
	}
	if err := s.Store.AppendStepLog(c.Request.Context(), job.ID, n, key, len(data)); err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "size": size})
}

// uploadStepSummary stores a step's GITHUB_STEP_SUMMARY markdown as a blob.
// GitHub caps summaries at 1 MiB per step; the same bound applies here and the
// content is masked like log output.
func (s *Server) uploadStepSummary(c *gin.Context) {
	job, _, ok := s.runnerJob(c)
	if !ok {
		return
	}
	n := parseInt(c.Param("n"), 0)
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_body", "summary too large or unreadable")
		return
	}
	if len(body) == 0 {
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return
	}
	masker := s.maskerForJob(c, job)
	ref := blobs.SummaryKey(job.ID.String(), n)
	if _, _, err := s.Blobs.Put(ref, bytes.NewReader(masker.MaskBytes(body))); err != nil {
		s.internalError(c, err)
		return
	}
	if err := s.Store.SetStepSummary(c.Request.Context(), job.ID, n, ref); err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// maskerForJob collects every secret value that could appear in this job's
// logs — project/org secrets plus the job's environment-scoped secrets.
func (s *Server) maskerForJob(c *gin.Context, job store.Job) *secrets.Masker {
	run, err := s.Store.Run(c.Request.Context(), job.RunID)
	if err != nil {
		return secrets.NewMasker(nil)
	}
	project, err := s.Store.ProjectByID(c.Request.Context(), run.ProjectID)
	if err != nil {
		return secrets.NewMasker(nil)
	}
	rows, err := s.Store.SecretsForProject(c.Request.Context(), project.OrgID, project.ID)
	if err != nil {
		return secrets.NewMasker(nil)
	}
	if job.Environment != "" {
		if envRows, err := s.Store.SecretsForEnvironment(c.Request.Context(), project.ID, job.Environment); err == nil {
			rows = append(rows, envRows...)
		}
	}
	values := make([]string, 0, len(rows))
	for _, sec := range rows {
		if plain, err := s.Cipher.Decrypt(sec.Ciphertext, sec.Nonce); err == nil {
			values = append(values, plain)
		}
	}
	return secrets.NewMasker(values)
}

type finishStepRequest struct {
	Conclusion  string          `json:"conclusion"`
	ExitCode    *int            `json:"exit_code"`
	Diagnosis   json.RawMessage `json:"diagnosis"`
	Annotations json.RawMessage `json:"annotations"`
}

func (s *Server) finishStep(c *gin.Context) {
	job, _, ok := s.runnerJob(c)
	if !ok {
		return
	}
	n := parseInt(c.Param("n"), 0)
	var req finishStepRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_body", "invalid finish body")
		return
	}
	switch req.Conclusion {
	case "success", "failure", "skipped", "cancelled":
	default:
		fail(c, http.StatusBadRequest, "invalid_conclusion", "conclusion must be success, failure, skipped or cancelled")
		return
	}
	// The diagnosis carries raw log tails (detail, agent_prompt) that bypass
	// the ingest-time masker — mask every string before it is persisted.
	diag := maskDiagnosis(req.Diagnosis, s.maskerForJob(c, job))
	// Annotations are job-authored text too — same masking pass as diagnosis.
	// Anything that is not a JSON array is dropped rather than poisoning the
	// jsonb column.
	anns := maskDiagnosis(req.Annotations, s.maskerForJob(c, job))
	var annList []any
	if json.Unmarshal(anns, &annList) != nil {
		annList = nil
	}
	if annList == nil {
		anns = json.RawMessage("[]")
	}
	if err := s.Store.FinishStep(c.Request.Context(), job.ID, n, req.Conclusion, req.ExitCode, diag, anns); err != nil {
		s.internalError(c, err)
		return
	}
	// Audit auto-remediations the runner reports applying.
	var d struct {
		Remediation *struct {
			Applied bool `json:"applied"`
		} `json:"remediation"`
	}
	if json.Unmarshal(diag, &d) == nil && d.Remediation != nil && d.Remediation.Applied {
		if run, err := s.Store.Run(c.Request.Context(), job.RunID); err == nil {
			if p, err := s.Store.ProjectByID(c.Request.Context(), run.ProjectID); err == nil {
				s.Store.Audit(c.Request.Context(), p.OrgID, nil, "remediation.apply",
					fmt.Sprintf("job %s step %d", job.ID, n))
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

type finishJobRequest struct {
	Conclusion string            `json:"conclusion"`
	Outputs    map[string]string `json:"outputs"`
	Error      string            `json:"error"`
}

func (s *Server) finishJob(c *gin.Context) {
	job, runner, ok := s.runnerJob(c)
	if !ok {
		return
	}
	var req finishJobRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_body", "invalid finish body")
		return
	}
	switch req.Conclusion {
	case "success", "failure", "cancelled", "skipped":
	default:
		fail(c, http.StatusBadRequest, "invalid_conclusion", "invalid conclusion")
		return
	}
	// Job errors surface in the dashboard and status payloads; mask like logs.
	req.Error = s.maskerForJob(c, job).Mask(req.Error)
	updated, err := s.Store.FinishJob(c.Request.Context(), runner.ID, job.ID, req.Conclusion, req.Error, req.Outputs)
	if err != nil {
		s.internalError(c, err)
		return
	}
	if !updated {
		fail(c, http.StatusConflict, "lease_lost", "job is no longer leased to this runner")
		return
	}
	if err := s.Orch.AdvanceRun(c.Request.Context(), job.RunID); err != nil {
		s.Log.Error("advance after job finish", zap.Error(err))
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) cancelCheck(c *gin.Context) {
	job, _, ok := s.runnerJob(c)
	if !ok {
		return
	}
	runStatus, conclusion, err := s.Store.RunStatus(c.Request.Context(), job.RunID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	current, err := s.Store.Job(c.Request.Context(), job.ID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	cancelled := current.Status == "cancelled" || runStatus == "cancelled" ||
		(runStatus == "completed" && conclusion == "cancelled")
	c.JSON(http.StatusOK, gin.H{"cancelled": cancelled})
}

// forkTokenPermissions is the forced GITHUB_TOKEN ceiling for untrusted
// (fork/pull_request) runs. Read-only scopes let API-backed actions work
// without letting untrusted code write to the repository.
var forkTokenPermissions = map[string]string{
	"contents":      "read",
	"pull-requests": "read",
	"issues":        "read",
}

// secretsEligibleTrust reports whether a run trust level may receive
// repository secrets and a workflow-ceilinged GITHUB_TOKEN. fork and
// pull_request runs are untrusted code: no secrets, and only the forced
// read-only forkTokenPermissions ceiling on their token.
func secretsEligibleTrust(level string) bool {
	return level != "fork" && level != "pull_request"
}

// jobSecrets delivers decrypted secret values only to trusted runs.
func (s *Server) jobSecrets(c *gin.Context) {
	job, _, ok := s.runnerJob(c)
	if !ok {
		return
	}
	run, err := s.Store.Run(c.Request.Context(), job.RunID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	project, err := s.Store.ProjectByID(c.Request.Context(), run.ProjectID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	// Vars are configuration, not credentials: they ride along even for
	// untrusted runs, matching GitHub's vars context semantics.
	varRows, err := s.Store.VarsForProject(c.Request.Context(), project.ID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	vars := make(map[string]string, len(varRows))
	for _, v := range varRows {
		vars[v.Name] = v.Value
	}
	if !secretsEligibleTrust(run.TrustLevel) {
		c.JSON(http.StatusOK, gin.H{"secrets": map[string]string{}, "vars": vars, "blocked": true,
			"reason": "untrusted pull request runs never receive repository secrets"})
		return
	}
	rows, err := s.Store.SecretsForProject(c.Request.Context(), project.OrgID, project.ID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	out := map[string]string{}
	decryptInto := func(rows []store.Secret) {
		for _, sec := range rows {
			plain, err := s.Cipher.Decrypt(sec.Ciphertext, sec.Nonce)
			if err != nil {
				s.Log.Error("decrypt secret", zap.String("secret", sec.Name), zap.Error(err))
				continue
			}
			out[sec.Name] = plain
		}
	}
	decryptInto(rows)
	// Environment secrets outrank project and org values for jobs that name
	// the environment.
	if job.Environment != "" {
		if envRows, err := s.Store.SecretsForEnvironment(c.Request.Context(), project.ID, job.Environment); err == nil {
			decryptInto(envRows)
		}
	}
	// Reusable-workflow callers can narrow what the called jobs see via
	// jobs.<id>.secrets: a map resolves each called name against the caller's
	// secrets/vars context here, server-side, so raw caller values never
	// leave the control plane under the wrong name. nil = full set,
	// empty = none.
	if sm, err := s.Store.JobSecretsMap(c.Request.Context(), job.ID); err != nil {
		s.Log.Warn("job secrets map", zap.String("job", job.ID.String()), zap.Error(err))
	} else if sm != nil {
		sctx := map[string]any{
			"secrets": stringAnyMap(out), "vars": stringAnyMap(vars),
		}
		filtered := make(map[string]string, len(sm))
		for name, expr := range sm {
			v := expr
			if r, rerr := expressions.Interpolate(expr, sctx); rerr == nil {
				v = r
			}
			filtered[name] = v
		}
		out = filtered
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"secrets": out, "vars": vars, "blocked": false})
}

// stringAnyMap widens map[string]string for the expressions context.
func stringAnyMap(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// maskDiagnosis masks every string inside a step diagnosis JSON blob before
// it is stored. Returns the input unchanged when it is not a JSON object.
func maskDiagnosis(raw json.RawMessage, m *secrets.Masker) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return raw
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	masked, err := json.Marshal(maskJSONStrings(v, m))
	if err != nil {
		return raw
	}
	return masked
}

// maskJSONStrings walks a decoded JSON value and masks each string it holds.
func maskJSONStrings(v any, m *secrets.Masker) any {
	switch t := v.(type) {
	case string:
		return m.Mask(t)
	case []any:
		for i, e := range t {
			t[i] = maskJSONStrings(e, m)
		}
		return t
	case map[string]any:
		for k, e := range t {
			t[k] = maskJSONStrings(e, m)
		}
		return t
	default:
		return v
	}
}

// runnerJob loads the job and verifies it is leased to the authenticated runner.
func (s *Server) runnerJob(c *gin.Context) (store.Job, store.Runner, bool) {
	runner, ok := currentRunner(c)
	if !ok {
		fail(c, http.StatusUnauthorized, "unauthenticated", "runner authentication required")
		return store.Job{}, store.Runner{}, false
	}
	id, err := uuid.Parse(c.Param("jobID"))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_id", "job id must be a uuid")
		return store.Job{}, store.Runner{}, false
	}
	job, err := s.Store.Job(c.Request.Context(), id)
	if err != nil {
		s.internalError(c, err)
		return store.Job{}, store.Runner{}, false
	}
	if job.RunnerID == nil || *job.RunnerID != runner.ID {
		fail(c, http.StatusNotFound, "not_found", "job is not leased to this runner")
		return store.Job{}, store.Runner{}, false
	}
	return job, runner, true
}
