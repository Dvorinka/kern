package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// JobSeed is a compiled job ready to be persisted.
type JobSeed struct {
	Key             string
	JobKey          string
	Name            string
	Needs           []string
	RunsOn          []string
	Image           string
	Matrix          map[string]any
	Env             map[string]string
	IfCondition     string
	TimeoutSeconds  int
	ContinueOnError bool
	FailFast        bool
	Priority        int
	OutputsSpec     map[string]string
	Inputs          map[string]string
	Services        []ServiceSeed
	Environment     string
	EnvironmentURL  string
	// Permissions is the job's GitHub Actions permissions ceiling (jsonb on
	// the jobs row); the claim path uses it to scope GITHUB_TOKEN.
	Permissions map[string]string
	// SplitN and SplitAuto are consumed before insert. They are not columns.
	SplitN    int
	SplitAuto bool
	// NeedsDocker marks jobs whose steps invoke the docker CLI or docker/*
	// actions; the claim query prefers kern-docker-capable runners for them.
	NeedsDocker bool
	// SecretsMap is the reusable-caller secrets spec: nil delivers the full
	// eligible set, an empty non-nil map delivers none, otherwise
	// called-name -> caller expression resolved at secrets-fetch time.
	SecretsMap map[string]string
	Steps      []StepSeed
}

// ServiceSeed is a service container spec persisted with the job.
type ServiceSeed struct {
	Name    string            `json:"name"`
	Image   string            `json:"image"`
	Env     map[string]string `json:"env,omitempty"`
	Options []string          `json:"options,omitempty"`
	Ports   []string          `json:"ports,omitempty"`
}

type StepSeed struct {
	Key  string
	Name string
	Spec any
}

type CreateRunParams struct {
	ProjectID         uuid.UUID
	WorkflowID        uuid.UUID
	WorkflowVersionID uuid.UUID
	Event             string
	Ref               string
	SHA               string
	CommitMessage     string
	Actor             string
	TrustLevel        string
	Source            map[string]any
	ConcurrencyGroup  string
	CancelInProgress  bool
	// Inputs carries workflow_dispatch inputs. They merge over each job's
	// plan inputs at seed time (not persisted on the run row — the merged
	// result lives on jobs.inputs).
	Inputs map[string]any
	// HoldForApproval parks the run in awaiting_approval: jobs stay 'waiting'
	// and nothing is promoted until ApproveRunHold releases it.
	HoldForApproval bool
}

// CreateRun persists a run and its compiled jobs atomically. Run numbers are allocated
// per workflow; a unique violation (concurrent insert) is retried.
func (s *Store) CreateRun(ctx context.Context, p CreateRunParams, jobs []JobSeed) (Run, error) {
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		run, err := s.createRunOnce(ctx, p, jobs)
		if err == nil {
			return run, nil
		}
		lastErr = err
	}
	return Run{}, fmt.Errorf("create run: %w", lastErr)
}

func (s *Store) createRunOnce(ctx context.Context, p CreateRunParams, jobs []JobSeed) (Run, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback(ctx)

	source, err := json.Marshal(p.Source)
	if err != nil {
		return Run{}, err
	}
	if p.TrustLevel == "" {
		p.TrustLevel = "trusted"
	}

	var id uuid.UUID
	status := "queued"
	if p.HoldForApproval {
		status = "awaiting_approval"
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO workflow_runs
			(project_id, workflow_id, workflow_version_id, run_number, event, ref, sha,
			 commit_message, actor, trust_level, source, concurrency_group, cancel_in_progress, status)
		VALUES ($1, $2, $3,
		        (SELECT COALESCE(MAX(run_number), 0) + 1 FROM workflow_runs WHERE workflow_id = $2),
		        $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING id`,
		p.ProjectID, p.WorkflowID, p.WorkflowVersionID, p.Event, p.Ref, p.SHA,
		p.CommitMessage, p.Actor, p.TrustLevel, source, p.ConcurrencyGroup, p.CancelInProgress,
		status).Scan(&id)
	if err != nil {
		return Run{}, err
	}

	for _, j := range jobs {
		matrix, err := json.Marshal(j.Matrix)
		if err != nil {
			return Run{}, err
		}
		env, err := json.Marshal(j.Env)
		if err != nil {
			return Run{}, err
		}
		outputsSpec, err := json.Marshal(j.OutputsSpec)
		if err != nil {
			return Run{}, err
		}
		inputs, err := json.Marshal(j.Inputs)
		if err != nil {
			return Run{}, err
		}
		services, err := json.Marshal(j.Services)
		if err != nil {
			return Run{}, err
		}
		permissions, err := json.Marshal(j.Permissions)
		if err != nil {
			return Run{}, err
		}
		secretsMap, err := json.Marshal(j.SecretsMap)
		if err != nil {
			return Run{}, err
		}
		if j.TimeoutSeconds <= 0 {
			j.TimeoutSeconds = 3600
		}
		var jobID uuid.UUID
		err = tx.QueryRow(ctx, `
			INSERT INTO jobs
				(run_id, job_key, key, name, status, needs, runs_on, image, matrix, env,
				 if_condition, timeout_seconds, continue_on_error, fail_fast, priority, outputs_spec,
				 environment, environment_url, inputs, services, permissions, needs_docker, secrets_map)
			VALUES ($1, $2, $3, $4, 'waiting', $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22)
			RETURNING id`,
			id, j.JobKey, j.Key, j.Name, j.Needs, j.RunsOn, j.Image, matrix, env,
			j.IfCondition, j.TimeoutSeconds, j.ContinueOnError, j.FailFast, j.Priority, outputsSpec,
			j.Environment, j.EnvironmentURL, inputs, services, permissions, j.NeedsDocker, secretsMap).Scan(&jobID)
		if err != nil {
			return Run{}, fmt.Errorf("insert job %s: %w", j.Key, err)
		}
		for i, st := range j.Steps {
			spec, err := json.Marshal(st.Spec)
			if err != nil {
				return Run{}, err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO steps (job_id, number, key, name, spec) VALUES ($1, $2, $3, $4, $5)`,
				jobID, i+1, st.Key, st.Name, spec); err != nil {
				return Run{}, fmt.Errorf("insert step %s: %w", st.Key, err)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Run{}, err
	}
	return s.Run(ctx, id)
}

const runSelect = `
	SELECT r.id, r.project_id, r.workflow_id, r.workflow_version_id, r.run_number, r.event,
	       r.ref, r.sha, r.commit_message, r.actor, r.status, r.conclusion, r.trust_level,
	       r.source, r.concurrency_group, r.started_at, r.completed_at, r.created_at, r.cleared_at,
	       w.name, w.path, p.slug, p.name,
	       COALESCE((SELECT j.classification FROM jobs j
	        WHERE j.run_id = r.id AND j.classification <> ''
	        ORDER BY j.queued_at, j.key LIMIT 1), '')
	FROM workflow_runs r
	JOIN workflows w ON w.id = r.workflow_id
	JOIN projects p ON p.id = r.project_id`

func scanRun(row pgx.Row) (Run, error) {
	var r Run
	err := row.Scan(&r.ID, &r.ProjectID, &r.WorkflowID, &r.WorkflowVersionID, &r.RunNumber,
		&r.Event, &r.Ref, &r.SHA, &r.CommitMessage, &r.Actor, &r.Status, &r.Conclusion,
		&r.TrustLevel, &r.Source, &r.ConcurrencyGroup, &r.StartedAt, &r.CompletedAt, &r.CreatedAt,
		&r.ClearedAt, &r.WorkflowName, &r.WorkflowPath, &r.ProjectSlug, &r.ProjectName,
		&r.Classification)
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	return r, err
}

func (s *Store) Run(ctx context.Context, id uuid.UUID) (Run, error) {
	return scanRun(s.pool.QueryRow(ctx, runSelect+` WHERE r.id = $1`, id))
}

// RunPrefix resolves an unambiguous run-id prefix — the short ids the CLI
// and UI print. Returns up to 8 candidates newest-first so callers can
// detect ambiguity; callers must treat len != 1 as unresolved.
func (s *Store) RunPrefix(ctx context.Context, prefix string) ([]Run, error) {
	rows, err := s.pool.Query(ctx, runSelect+`
		WHERE r.id::text LIKE $1 || '%'
		ORDER BY r.created_at DESC LIMIT 8`, prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type RunFilter struct {
	ProjectID *uuid.UUID
	// Status accepts a comma-separated list (e.g. "queued,in_progress").
	Status         string
	Conclusion     string
	IncludeCleared bool
	// Ref matches the run ref exactly or as refs/heads/<ref> — `--branch main`
	// and `--ref refs/pull/3/head` both work.
	Ref string
	// PR matches source.pr_number (webhook and manual PR dispatches).
	PR string
	// Workflow matches the workflow name or path (.github/workflows/ci.yml).
	Workflow string
	// Since/Until bound created_at — the chart's day drill-down.
	Since, Until time.Time
	Limit        int
	Offset       int
}

func (s *Store) Runs(ctx context.Context, f RunFilter) ([]Run, int, error) {
	limit, offset := page(f.Limit, f.Offset)
	var statuses []string
	for _, st := range strings.Split(f.Status, ",") {
		if st = strings.TrimSpace(st); st != "" {
			statuses = append(statuses, st)
		}
	}
	rows, err := s.pool.Query(ctx, runSelect+`
		WHERE ($1::uuid IS NULL OR r.project_id = $1)
		  AND ($2::text[] IS NULL OR r.status = ANY($2))
		  AND ($3 = '' OR r.conclusion = $3)
		  AND ($4::bool OR r.cleared_at IS NULL)
		  AND ($7 = '' OR r.ref = $7 OR r.ref = 'refs/heads/' || $7)
		  AND ($8 = '' OR r.source->>'pr_number' = $8)
		  AND ($9 = '' OR w.name = $9 OR w.path = $9 OR w.path LIKE '%/' || $9)
		  AND ($10::timestamptz IS NULL OR r.created_at >= $10)
		  AND ($11::timestamptz IS NULL OR r.created_at < $11)
		ORDER BY r.created_at DESC
		LIMIT $5 OFFSET $6`, f.ProjectID, statuses, f.Conclusion, f.IncludeCleared, limit, offset,
		f.Ref, f.PR, f.Workflow, nilTime(f.Since), nilTime(f.Until))
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	var total int
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM workflow_runs r
		WHERE ($1::uuid IS NULL OR r.project_id = $1)
		  AND ($2::text[] IS NULL OR r.status = ANY($2))
		  AND ($3 = '' OR r.conclusion = $3)
		  AND ($4::bool OR r.cleared_at IS NULL)
		  AND ($5::timestamptz IS NULL OR r.created_at >= $5)
		  AND ($6::timestamptz IS NULL OR r.created_at < $6)`,
		f.ProjectID, statuses, f.Conclusion, f.IncludeCleared,
		nilTime(f.Since), nilTime(f.Until)).Scan(&total); err != nil {
		return nil, 0, err
	}
	return out, total, rows.Err()
}

// nilTime returns nil for the zero time so optional filters stay
// parameterizable — postgres compares $N::timestamptz IS NULL first.
func nilTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// RunCounts returns per-tab totals for the runs list, ignoring status and
// conclusion filters but honouring project scope and the cleared flag.
// success/completed/median_seconds let the UI compute health stats over the
// whole history instead of the page it happened to load.
func (s *Store) RunCounts(ctx context.Context, projectID *uuid.UUID, includeCleared bool) (map[string]int, error) {
	var all, running, failed, cancelled, completed, success, median int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE r.status IN ('queued', 'in_progress')),
		       count(*) FILTER (WHERE r.conclusion = 'failure'),
		       count(*) FILTER (WHERE r.status = 'cancelled' OR r.conclusion = 'cancelled'),
		       count(*) FILTER (WHERE r.status = 'completed'),
		       count(*) FILTER (WHERE r.conclusion = 'success'),
		       COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM r.completed_at - r.started_at))
		                FILTER (WHERE r.status = 'completed' AND r.started_at IS NOT NULL AND r.completed_at IS NOT NULL), 0)::int
		FROM workflow_runs r
		WHERE ($1::uuid IS NULL OR r.project_id = $1)
		  AND ($2::bool OR r.cleared_at IS NULL)`,
		projectID, includeCleared).Scan(&all, &running, &failed, &cancelled, &completed, &success, &median)
	if err != nil {
		return nil, err
	}
	return map[string]int{"all": all, "running": running, "failed": failed, "cancelled": cancelled,
		"completed": completed, "success": success, "median_seconds": median}, nil
}

// ClearRun marks a single cancelled run cleared. Returns false when the run
// is not a clearable cancelled run.
func (s *Store) ClearRun(ctx context.Context, runID uuid.UUID) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE workflow_runs SET cleared_at = now()
		WHERE id = $1 AND status = 'cancelled' AND cleared_at IS NULL`, runID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ClearCancelledRuns clears every cancelled run in the org (optionally scoped
// to one project) and returns how many were cleared.
func (s *Store) ClearCancelledRuns(ctx context.Context, orgID uuid.UUID, projectID *uuid.UUID) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE workflow_runs r SET cleared_at = now()
		FROM projects p
		WHERE p.id = r.project_id AND p.org_id = $1
		  AND r.status = 'cancelled' AND r.cleared_at IS NULL
		  AND ($2::uuid IS NULL OR r.project_id = $2)`, orgID, projectID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (s *Store) JobsForRun(ctx context.Context, runID uuid.UUID) ([]Job, error) {
	return s.jobsForRun(ctx, runID, true)
}

// JobsForRunNoSteps is JobsForRun without the per-job step queries. Scheduler
// paths (AdvanceRun, claim payload needs-context) read only job state, and
// Tick calls them once per active run — the steps join was pure overhead.
func (s *Store) JobsForRunNoSteps(ctx context.Context, runID uuid.UUID) ([]Job, error) {
	return s.jobsForRun(ctx, runID, false)
}

func (s *Store) jobsForRun(ctx context.Context, runID uuid.UUID, withSteps bool) ([]Job, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, run_id, job_key, key, name, status, conclusion, needs, runs_on, image, matrix, env,
		       if_condition, timeout_seconds, continue_on_error, fail_fast, priority, attempt, runner_id,
		       queued_at, started_at, completed_at, lease_expires_at, error, outputs, outputs_spec,
		       environment, environment_url, inputs, services, permissions, metrics, classification
		FROM jobs WHERE run_id = $1 ORDER BY queued_at, key`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !withSteps {
		return out, nil
	}
	for i := range out {
		steps, err := s.StepsForJob(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Steps = steps
	}
	return out, nil
}

func (s *Store) StepsForJob(ctx context.Context, jobID uuid.UUID) ([]Step, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, job_id, number, key, name, status, conclusion, exit_code, started_at,
		       completed_at, spec, log_ref, log_bytes, diagnosis, annotations, summary_ref
		FROM steps WHERE job_id = $1 ORDER BY number`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Step{}
	for rows.Next() {
		var st Step
		if err := rows.Scan(&st.ID, &st.JobID, &st.Number, &st.Key, &st.Name, &st.Status,
			&st.Conclusion, &st.ExitCode, &st.StartedAt, &st.CompletedAt, &st.Spec, &st.LogRef,
			&st.LogBytes, &st.Diagnosis, &st.Annotations, &st.SummaryRef); err != nil {
			return nil, err
		}
		st.HasSummary = st.SummaryRef != ""
		out = append(out, st)
	}
	return out, rows.Err()
}

func scanJob(row pgx.Row) (Job, error) {
	var j Job
	err := row.Scan(&j.ID, &j.RunID, &j.JobKey, &j.Key, &j.Name, &j.Status, &j.Conclusion, &j.Needs,
		&j.RunsOn, &j.Image, &j.Matrix, &j.Env, &j.IfCondition, &j.TimeoutSeconds,
		&j.ContinueOnError, &j.FailFast, &j.Priority, &j.Attempt, &j.RunnerID, &j.QueuedAt,
		&j.StartedAt, &j.CompletedAt, &j.LeaseExpiresAt, &j.Error, &j.Outputs, &j.OutputsSpec,
		&j.Environment, &j.EnvironmentURL, &j.Inputs, &j.Services, &j.Permissions, &j.Metrics,
		&j.Classification)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	return j, err
}

func (s *Store) Job(ctx context.Context, id uuid.UUID) (Job, error) {
	j, err := scanJob(s.pool.QueryRow(ctx, `
		SELECT j.id, j.run_id, j.job_key, j.key, j.name, j.status, j.conclusion, j.needs, j.runs_on,
		       j.image, j.matrix, j.env, j.if_condition, j.timeout_seconds, j.continue_on_error,
		       j.fail_fast, j.priority, j.attempt, j.runner_id, j.queued_at, j.started_at, j.completed_at,
		       j.lease_expires_at, j.error, j.outputs, j.outputs_spec, j.environment, j.environment_url,
		       j.inputs, j.services, j.permissions, j.metrics, j.classification
		FROM jobs j WHERE j.id = $1`, id))
	if err != nil {
		return Job{}, err
	}
	j.Steps, err = s.StepsForJob(ctx, j.ID)
	return j, err
}

// CancelRun marks a run and its non-terminal jobs cancelled.
func (s *Store) CancelRun(ctx context.Context, runID uuid.UUID) (Run, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		UPDATE workflow_runs SET status = 'cancelled', conclusion = 'cancelled', completed_at = now()
		WHERE id = $1 AND status NOT IN ('completed', 'cancelled')`, runID); err != nil {
		return Run{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE jobs SET status = 'cancelled', conclusion = 'cancelled', completed_at = now()
		WHERE run_id = $1 AND status IN ('queued', 'waiting', 'waiting_approval', 'in_progress')`, runID); err != nil {
		return Run{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE deployments SET status = 'failure', completed_at = now()
		WHERE run_id = $1 AND status IN ('queued', 'in_progress')`, runID); err != nil {
		return Run{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE steps SET status = 'cancelled', conclusion = 'cancelled', completed_at = now()
		WHERE status IN ('queued', 'running')
		  AND job_id IN (SELECT id FROM jobs WHERE run_id = $1)`, runID); err != nil {
		return Run{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE job_leases SET released_at = now() WHERE released_at IS NULL
		  AND job_id IN (SELECT id FROM jobs WHERE run_id = $1)`, runID); err != nil {
		return Run{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Run{}, err
	}
	return s.Run(ctx, runID)
}

// rerunJob is the minimal job shape in-place rerun selection needs.
type rerunJob struct {
	ID         uuid.UUID
	JobKey     string
	Key        string
	Needs      []string
	Conclusion string
}

// rerunSeed picks the jobs an in-place rerun resets. keys non-empty selects
// those jobs (matched on the unique key, then the shared job_key so a matrix
// id selects the whole group); empty selects every job that did not succeed.
// All transitive dependents join regardless of conclusion — a rerun can
// change a job's outputs, so downstream results are stale by definition.
func rerunSeed(jobs []rerunJob, keys []string) map[uuid.UUID]bool {
	want := make(map[string]bool, len(keys))
	for _, k := range keys {
		want[k] = true
	}
	seed := map[uuid.UUID]bool{}
	seedKeys := map[string]bool{}
	for _, j := range jobs {
		var hit bool
		if len(keys) == 0 {
			hit = j.Conclusion == "failure" || j.Conclusion == "cancelled"
		} else {
			hit = want[j.Key] || want[j.JobKey]
		}
		if hit {
			seed[j.ID] = true
			seedKeys[j.JobKey] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for _, j := range jobs {
			if seed[j.ID] {
				continue
			}
			for _, n := range j.Needs {
				if seedKeys[n] {
					seed[j.ID] = true
					seedKeys[j.JobKey] = true
					changed = true
					break
				}
			}
		}
	}
	return seed
}

// RerunJobs resets selected jobs of a finished run back to 'waiting' so the
// orchestrator re-evaluates needs/if/environments exactly like a fresh run,
// and flips the run back to 'in_progress'. keys empty means "everything that
// did not succeed" (gh rerun --failed); otherwise the named job keys plus
// their dependents (gh rerun --job). Returns the number of jobs reset.
func (s *Store) RerunJobs(ctx context.Context, runID uuid.UUID, keys []string) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		UPDATE workflow_runs SET status = 'in_progress', conclusion = '',
			completed_at = NULL, started_at = COALESCE(started_at, now())
		WHERE id = $1 AND status IN ('completed', 'cancelled')`, runID)
	if err != nil {
		return 0, err
	}
	if tag.RowsAffected() == 0 {
		return 0, fmt.Errorf("run %s is not finished", runID)
	}

	rows, err := tx.Query(ctx, `
		SELECT id, job_key, key, needs, conclusion FROM jobs WHERE run_id = $1`, runID)
	if err != nil {
		return 0, err
	}
	var jobs []rerunJob
	for rows.Next() {
		var j rerunJob
		if err := rows.Scan(&j.ID, &j.JobKey, &j.Key, &j.Needs, &j.Conclusion); err != nil {
			rows.Close()
			return 0, err
		}
		jobs = append(jobs, j)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	seed := rerunSeed(jobs, keys)
	if len(seed) == 0 {
		return 0, fmt.Errorf("no jobs to rerun")
	}
	ids := make([]uuid.UUID, 0, len(seed))
	for id := range seed {
		ids = append(ids, id)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE jobs SET status = 'waiting', conclusion = '', error = '',
			runner_id = NULL, lease_expires_at = NULL, started_at = NULL,
			completed_at = NULL, attempt = 0, outputs = '{}'::jsonb
		WHERE id = ANY($1)`, ids); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE steps SET status = 'queued', conclusion = '', exit_code = NULL,
			started_at = NULL, completed_at = NULL, log_ref = '', log_bytes = 0,
			diagnosis = NULL, annotations = '[]'::jsonb, summary_ref = ''
		WHERE job_id = ANY($1)`, ids); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE job_leases SET released_at = now()
		WHERE released_at IS NULL AND job_id = ANY($1)`, ids); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(ids), nil
}

// PromoteQueuedRuns moves serialized runs (concurrency groups) forward when the
// group is free. When a newer queued run shares a group with an in-progress run
// that opted into cancel-in-progress, the older run is cancelled first.
func (s *Store) PromoteQueuedRuns(ctx context.Context) (int, error) {
	dead, err := s.pool.Query(ctx, `
		UPDATE workflow_runs SET status = 'cancelled', conclusion = 'cancelled', completed_at = now()
		WHERE id IN (
			SELECT o.id FROM workflow_runs o
			JOIN workflow_runs n ON n.project_id = o.project_id
				AND n.concurrency_group = o.concurrency_group
				AND o.concurrency_group <> ''
				AND n.status = 'queued'
				AND (n.created_at, n.id) > (o.created_at, o.id)
			WHERE o.status IN ('queued', 'in_progress')
			  AND o.cancel_in_progress)
		RETURNING id`)
	if err != nil {
		return 0, err
	}
	var deadIDs []uuid.UUID
	for dead.Next() {
		var id uuid.UUID
		if err := dead.Scan(&id); err != nil {
			dead.Close()
			return 0, err
		}
		deadIDs = append(deadIDs, id)
	}
	dead.Close()
	if err := dead.Err(); err != nil {
		return 0, err
	}
	for _, runID := range deadIDs {
		// Jobs of a superseded run were previously left dangling; cancel them
		// and their open deployments alongside the run.
		if _, err := s.pool.Exec(ctx, `
			UPDATE jobs SET status = 'cancelled', conclusion = 'cancelled', completed_at = now()
			WHERE run_id = $1 AND status IN ('queued', 'waiting', 'waiting_approval', 'in_progress')`, runID); err != nil {
			return 0, err
		}
		if _, err := s.pool.Exec(ctx, `
			UPDATE deployments SET status = 'failure', completed_at = now()
			WHERE run_id = $1 AND status IN ('queued', 'in_progress')`, runID); err != nil {
			return 0, err
		}
		if _, err := s.pool.Exec(ctx, `
			UPDATE job_leases SET released_at = now() WHERE released_at IS NULL
			  AND job_id IN (SELECT id FROM jobs WHERE run_id = $1)`, runID); err != nil {
			return 0, err
		}
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE workflow_runs r SET status = 'in_progress', started_at = now()
		WHERE r.status = 'queued'
		  AND NOT EXISTS (
			SELECT 1 FROM workflow_runs o
			WHERE o.project_id = r.project_id
			  AND o.concurrency_group = r.concurrency_group
			  AND o.concurrency_group <> ''
			  AND o.id <> r.id
			  AND o.status IN ('queued', 'in_progress')
			  AND (o.created_at, o.id) < (r.created_at, r.id)
		  )`)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// ApproveRunHold releases a run parked in awaiting_approval back to queued;
// PromoteQueuedRuns then schedules it like any other run.
func (s *Store) ApproveRunHold(ctx context.Context, runID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE workflow_runs SET status = 'queued'
		WHERE id = $1 AND status = 'awaiting_approval'`, runID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("run %s is not awaiting approval", runID)
	}
	return nil
}

// ActiveRuns returns the count of runs that have not reached a terminal status.
func (s *Store) ActiveRuns(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM workflow_runs WHERE status IN ('awaiting_approval','queued','in_progress')`).Scan(&n)
	return n, err
}

func (s *Store) FinishRunIfDone(ctx context.Context, runID uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var pending int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM jobs WHERE run_id = $1 AND status IN ('queued','waiting','waiting_approval','in_progress')`,
		runID).Scan(&pending); err != nil {
		return err
	}
	if pending > 0 {
		return nil
	}
	// Conclusion: failure wins, then cancelled, then success. Skipped-only runs
	// are reported skipped. A failed continue-on-error job keeps its stored
	// conclusion (the UI shows it failed) but, per GitHub semantics, must not
	// fail the run.
	if _, err := tx.Exec(ctx, `
		UPDATE workflow_runs SET
			status = 'completed',
			conclusion = CASE
				WHEN EXISTS (SELECT 1 FROM jobs WHERE run_id = $1
					AND conclusion = 'failure' AND NOT continue_on_error) THEN 'failure'
				WHEN EXISTS (SELECT 1 FROM jobs WHERE run_id = $1 AND conclusion = 'cancelled') THEN 'cancelled'
				WHEN NOT EXISTS (SELECT 1 FROM jobs WHERE run_id = $1
					AND (conclusion = 'success' OR (conclusion = 'failure' AND continue_on_error))) THEN 'skipped'
				ELSE 'success'
			END,
			completed_at = now()
		WHERE id = $1 AND status = 'in_progress'`, runID); err != nil {
		return err
	}
	// A completed run can never consume an approved remediation — withdraw
	// outstanding proposals so they stop asking for a decision that cannot
	// be acted on.
	if _, err := tx.Exec(ctx, `
		UPDATE steps s SET diagnosis = jsonb_set(s.diagnosis, '{remediation,dismissed}', 'true')
		FROM jobs j
		WHERE s.job_id = j.id AND j.run_id = $1
		  AND s.diagnosis->'remediation' IS NOT NULL
		  AND coalesce(s.diagnosis->'remediation'->>'approved', 'false') = 'false'
		  AND coalesce(s.diagnosis->'remediation'->>'applied', 'false') = 'false'
		  AND coalesce(s.diagnosis->'remediation'->>'dismissed', 'false') = 'false'`, runID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// NextRunNumber is exposed for tests and run planning.
func (s *Store) NextRunNumber(ctx context.Context, workflowID uuid.UUID) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(MAX(run_number), 0) + 1 FROM workflow_runs WHERE workflow_id = $1`,
		workflowID).Scan(&n)
	return n, err
}

// DayCount is one day×project bucket of run history for the overview
// chart — computed in postgres so history does not depend on how many
// runs the list endpoint happened to return.
type DayCount struct {
	Day       string `json:"day"`
	Project   string `json:"project"`
	Total     int    `json:"total"`
	Failed    int    `json:"failed"`
	Cancelled int    `json:"cancelled"`
	Live      int    `json:"live"`
}

// RunDayCounts aggregates runs per day per project over the last `days`
// days. Cleared runs stay counted — they are history, just hidden.
func (s *Store) RunDayCounts(ctx context.Context, projectID *uuid.UUID, days int) ([]DayCount, error) {
	if days <= 0 || days > 400 {
		days = 30
	}
	rows, err := s.pool.Query(ctx, `
		SELECT (r.created_at AT TIME ZONE 'UTC')::date::text, p.slug,
		       count(*),
		       count(*) FILTER (WHERE r.conclusion = 'failure'),
		       count(*) FILTER (WHERE r.conclusion = 'cancelled' OR r.status = 'cancelled'),
		       count(*) FILTER (WHERE r.status IN ('queued', 'in_progress', 'awaiting_approval'))
		FROM workflow_runs r JOIN projects p ON p.id = r.project_id
		WHERE ($1::uuid IS NULL OR r.project_id = $1)
		  AND r.created_at >= now() - make_interval(days => $2)
		GROUP BY 1, 2
		ORDER BY 1`, projectID, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DayCount
	for rows.Next() {
		var d DayCount
		if err := rows.Scan(&d.Day, &d.Project, &d.Total, &d.Failed, &d.Cancelled, &d.Live); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// QueuedJob is one parked job with just enough run context for the queue
// panels — the UI used to derive this by fanning out run-detail reads.
type QueuedJob struct {
	ID          uuid.UUID `json:"id"`
	RunID       uuid.UUID `json:"run_id"`
	RunNumber   int       `json:"run_number"`
	ProjectSlug string    `json:"project_slug"`
	JobKey      string    `json:"job_key"`
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	Needs       []string  `json:"needs"`
	RunsOn      []string  `json:"runs_on"`
	Priority    int       `json:"priority"`
	QueuedAt    time.Time `json:"queued_at"`
}

// QueuedJobs returns jobs waiting for a runner, oldest first — the order
// claims would serve them modulo labels and priority.
func (s *Store) QueuedJobs(ctx context.Context, limit int) ([]QueuedJob, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT j.id, j.run_id, r.run_number, p.slug, j.job_key, j.name, j.status,
		       j.needs, j.runs_on, j.priority, j.queued_at
		FROM jobs j
		JOIN workflow_runs r ON r.id = j.run_id
		JOIN projects p ON p.id = r.project_id
		WHERE j.status IN ('queued', 'waiting', 'waiting_approval')
		ORDER BY j.priority DESC, j.queued_at
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QueuedJob
	for rows.Next() {
		var q QueuedJob
		if err := rows.Scan(&q.ID, &q.RunID, &q.RunNumber, &q.ProjectSlug, &q.JobKey, &q.Name,
			&q.Status, &q.Needs, &q.RunsOn, &q.Priority, &q.QueuedAt); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}
