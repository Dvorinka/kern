-- +goose Up
-- Hot-path indexes (#374): every 500ms ClaimJob, runner heartbeat,
-- lease reap, cache lookup, and expiry sweep was seq-scanning an
-- unbounded table or a non-covering index.

-- ClaimJob's anti-affinity lookup reads the last lease per candidate
-- job (job_id ORDER BY created_at DESC) — previously a seq scan of the
-- unbounded job_leases table per candidate.
CREATE INDEX job_leases_job_idx ON job_leases (job_id, created_at DESC);

-- org_members PK is (org_id, user_id); auth-path joins by user_id
-- alone scanned the table.
CREATE INDEX org_members_user_idx ON org_members (user_id, created_at);

-- ClaimJob ordering (status, priority, est_seconds, queued_at) plus
-- GIN on runs_on label containment and partial indexes for the lease
-- reaper and timeout sweep on in-progress rows.
CREATE INDEX jobs_claim_order_idx ON jobs (status, priority DESC, est_seconds DESC, queued_at);
CREATE INDEX jobs_runs_on_gin ON jobs USING gin (runs_on);
CREATE INDEX jobs_lease_reap_idx ON jobs (lease_expires_at) WHERE status = 'in_progress';
CREATE INDEX jobs_timeout_idx ON jobs (started_at) WHERE status = 'in_progress';

-- cache_entries: restore-key prefix matching (starts_with(key)) plus
-- the 2s eviction sweeps on last_used_at/expires_at.
CREATE INDEX cache_entries_lookup_idx ON cache_entries
    (project_id, scope, key text_pattern_ops, created_at DESC);
CREATE INDEX cache_entries_last_used_idx ON cache_entries (last_used_at);
CREATE INDEX cache_entries_expires_idx ON cache_entries (expires_at) WHERE expires_at IS NOT NULL;

-- Artifact expiry sweep every 2s.
CREATE INDEX artifacts_expires_idx ON artifacts (expires_at) WHERE expires_at IS NOT NULL;

-- workflow_runs: concurrency-group dedupe, SHA lookup, workflow history.
CREATE INDEX workflow_runs_concurrency_idx ON workflow_runs
    (project_id, concurrency_group, status, created_at, id);
CREATE INDEX workflow_runs_sha_idx ON workflow_runs (sha) WHERE sha <> '';
CREATE INDEX workflow_runs_workflow_idx ON workflow_runs (workflow_id, created_at DESC);

-- Latest workflow version lookup.
CREATE INDEX workflow_versions_latest_idx ON workflow_versions (workflow_id, created_at DESC);

-- StepMedians/histograms over completed steps; log_ref presence for
-- PruneStepBlobs.
CREATE INDEX steps_completed_idx ON steps (completed_at) WHERE status = 'completed';
CREATE INDEX steps_log_ref_idx ON steps (job_id) WHERE log_ref <> '';

-- Case-insensitive repo_full_name lookup.
CREATE INDEX projects_repo_lower_idx ON projects (LOWER(repo_full_name));

-- Webhook delivery ordering and replay lookup.
CREATE INDEX webhook_deliveries_received_idx ON webhook_deliveries (received_at DESC);

-- Deployments by run; the approval/wait sweep only reads waiting rows.
CREATE INDEX deployments_run_idx ON deployments (run_id);
CREATE INDEX deployments_wait_idx ON deployments (status, wait_until)
    WHERE wait_until IS NOT NULL;

-- Per-user session and token lookups.
CREATE INDEX sessions_user_idx ON sessions (user_id);
CREATE INDEX api_tokens_user_idx ON api_tokens (user_id);

-- +goose Down
DROP INDEX api_tokens_user_idx;
DROP INDEX sessions_user_idx;
DROP INDEX deployments_wait_idx;
DROP INDEX deployments_run_idx;
DROP INDEX webhook_deliveries_received_idx;
DROP INDEX projects_repo_lower_idx;
DROP INDEX steps_log_ref_idx;
DROP INDEX steps_completed_idx;
DROP INDEX workflow_versions_latest_idx;
DROP INDEX workflow_runs_workflow_idx;
DROP INDEX workflow_runs_sha_idx;
DROP INDEX workflow_runs_concurrency_idx;
DROP INDEX artifacts_expires_idx;
DROP INDEX cache_entries_expires_idx;
DROP INDEX cache_entries_last_used_idx;
DROP INDEX cache_entries_lookup_idx;
DROP INDEX jobs_timeout_idx;
DROP INDEX jobs_lease_reap_idx;
DROP INDEX jobs_runs_on_gin;
DROP INDEX jobs_claim_order_idx;
DROP INDEX org_members_user_idx;
DROP INDEX job_leases_job_idx;
