-- +goose Up
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE orgs (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug       text NOT NULL UNIQUE,
    name       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email         text NOT NULL UNIQUE,
    name          text NOT NULL DEFAULT '',
    password_hash text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE org_members (
    org_id     uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role       text NOT NULL DEFAULT 'member' CHECK (role IN ('owner', 'admin', 'member')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, user_id)
);

CREATE TABLE sessions (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash text NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);

CREATE TABLE api_tokens (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name        text NOT NULL,
    prefix      text NOT NULL,
    token_hash  text NOT NULL UNIQUE,
    scopes      text[] NOT NULL DEFAULT '{read}',
    last_used_at timestamptz,
    expires_at  timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE projects (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id         uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    slug           text NOT NULL,
    name           text NOT NULL,
    provider       text NOT NULL DEFAULT 'github',
    repo_full_name text NOT NULL DEFAULT '',
    repo_url       text NOT NULL,
    default_branch text NOT NULL DEFAULT 'main',
    -- local_path enables workflow discovery from a checkout on the control-plane host.
    local_path     text NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, slug)
);

CREATE TABLE installations (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id        uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    provider      text NOT NULL,
    external_id   bigint NOT NULL,
    account_login text NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider, external_id)
);

CREATE TABLE webhook_deliveries (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    provider     text NOT NULL,
    delivery_id  text NOT NULL,
    event        text NOT NULL,
    payload      jsonb NOT NULL,
    status       text NOT NULL DEFAULT 'received'
                 CHECK (status IN ('received', 'processed', 'ignored', 'failed')),
    error        text NOT NULL DEFAULT '',
    received_at  timestamptz NOT NULL DEFAULT now(),
    processed_at timestamptz,
    UNIQUE (provider, delivery_id)
);

CREATE TABLE workflows (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    path       text NOT NULL,
    name       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project_id, path)
);

CREATE TABLE workflow_versions (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workflow_id uuid NOT NULL REFERENCES workflows (id) ON DELETE CASCADE,
    commit_sha  text NOT NULL,
    content     text NOT NULL,
    parsed      jsonb,
    valid       boolean NOT NULL DEFAULT false,
    diagnostics jsonb NOT NULL DEFAULT '[]',
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workflow_id, commit_sha)
);

CREATE TABLE workflow_runs (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id          uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    workflow_id         uuid NOT NULL REFERENCES workflows (id) ON DELETE CASCADE,
    workflow_version_id uuid NOT NULL REFERENCES workflow_versions (id),
    run_number          integer NOT NULL,
    event               text NOT NULL,
    ref                 text NOT NULL,
    sha                 text NOT NULL DEFAULT '',
    commit_message      text NOT NULL DEFAULT '',
    actor               text NOT NULL DEFAULT '',
    status              text NOT NULL DEFAULT 'queued'
                        CHECK (status IN ('queued', 'in_progress', 'completed', 'cancelled')),
    conclusion          text NOT NULL DEFAULT ''
                        CHECK (conclusion IN ('', 'success', 'failure', 'cancelled', 'skipped')),
    trust_level         text NOT NULL DEFAULT 'trusted'
                        CHECK (trust_level IN ('trusted', 'protected', 'pull_request', 'fork', 'deployment')),
    source              jsonb NOT NULL DEFAULT '{}',
    concurrency_group   text NOT NULL DEFAULT '',
    started_at          timestamptz,
    completed_at        timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workflow_id, run_number)
);
CREATE INDEX workflow_runs_project_created_idx ON workflow_runs (project_id, created_at DESC);
CREATE INDEX workflow_runs_status_idx ON workflow_runs (status, created_at);

CREATE TABLE jobs (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id           uuid NOT NULL REFERENCES workflow_runs (id) ON DELETE CASCADE,
    -- job_key is the job id from the YAML (stable across matrix expansion);
    -- key is unique per run (job_key plus matrix suffix when expanded).
    job_key          text NOT NULL,
    key              text NOT NULL,
    name             text NOT NULL,
    status           text NOT NULL DEFAULT 'queued'
                     CHECK (status IN ('queued', 'waiting', 'in_progress', 'completed', 'cancelled', 'skipped')),
    conclusion       text NOT NULL DEFAULT ''
                     CHECK (conclusion IN ('', 'success', 'failure', 'cancelled', 'skipped')),
    needs            text[] NOT NULL DEFAULT '{}',
    runs_on          text[] NOT NULL DEFAULT '{}',
    image            text NOT NULL DEFAULT '',
    matrix           jsonb NOT NULL DEFAULT '{}',
    env              jsonb NOT NULL DEFAULT '{}',
    if_condition     text NOT NULL DEFAULT '',
    timeout_seconds  integer NOT NULL DEFAULT 3600,
    continue_on_error boolean NOT NULL DEFAULT false,
    fail_fast        boolean NOT NULL DEFAULT true,
    priority         integer NOT NULL DEFAULT 0,
    attempt          integer NOT NULL DEFAULT 0,
    runner_id        uuid,
    queued_at        timestamptz NOT NULL DEFAULT now(),
    started_at       timestamptz,
    completed_at     timestamptz,
    lease_expires_at timestamptz,
    error            text NOT NULL DEFAULT '',
    outputs          jsonb NOT NULL DEFAULT '{}',
    -- outputs_spec maps declared job output names to expressions (evaluated by the runner).
    outputs_spec     jsonb NOT NULL DEFAULT '{}',
    UNIQUE (run_id, key)
);
CREATE INDEX jobs_queue_idx ON jobs (status, queued_at);
CREATE INDEX jobs_run_idx ON jobs (run_id);
CREATE INDEX jobs_runner_idx ON jobs (runner_id);

CREATE TABLE steps (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id       uuid NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    number       integer NOT NULL,
    key          text NOT NULL,
    name         text NOT NULL,
    status       text NOT NULL DEFAULT 'queued'
                 CHECK (status IN ('queued', 'running', 'completed', 'skipped', 'cancelled')),
    conclusion   text NOT NULL DEFAULT ''
                 CHECK (conclusion IN ('', 'success', 'failure', 'cancelled', 'skipped')),
    exit_code    integer,
    started_at   timestamptz,
    completed_at timestamptz,
    -- spec is the compiled StepPlan (run script, shell, env) owned by the control plane.
    spec         jsonb NOT NULL DEFAULT '{}',
    log_ref      text NOT NULL DEFAULT '',
    log_bytes    bigint NOT NULL DEFAULT 0,
    UNIQUE (job_id, number)
);

-- runners is referenced by jobs above; created after to keep the forward reference explicit.
CREATE TABLE runners (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id            uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    name              text NOT NULL,
    hostname          text NOT NULL DEFAULT '',
    os                text NOT NULL DEFAULT 'linux',
    arch              text NOT NULL DEFAULT 'amd64',
    cpus              integer NOT NULL DEFAULT 1,
    memory_bytes      bigint NOT NULL DEFAULT 0,
    disk_bytes        bigint NOT NULL DEFAULT 0,
    labels            text[] NOT NULL DEFAULT '{}',
    version           text NOT NULL DEFAULT '',
    status            text NOT NULL DEFAULT 'offline'
                      CHECK (status IN ('online', 'offline', 'draining')),
    draining          boolean NOT NULL DEFAULT false,
    ephemeral         boolean NOT NULL DEFAULT true,
    token_hash        text NOT NULL UNIQUE,
    last_heartbeat_at timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX runners_status_idx ON runners (status, last_heartbeat_at);

ALTER TABLE jobs ADD CONSTRAINT jobs_runner_fk FOREIGN KEY (runner_id) REFERENCES runners (id) ON DELETE SET NULL;

CREATE TABLE job_leases (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id      uuid NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    runner_id   uuid NOT NULL REFERENCES runners (id) ON DELETE CASCADE,
    expires_at  timestamptz NOT NULL,
    released_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX job_leases_active_idx ON job_leases (expires_at) WHERE released_at IS NULL;

CREATE TABLE artifacts (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id     uuid NOT NULL REFERENCES workflow_runs (id) ON DELETE CASCADE,
    job_id     uuid REFERENCES jobs (id) ON DELETE SET NULL,
    name       text NOT NULL,
    path       text NOT NULL DEFAULT '',
    size_bytes bigint NOT NULL DEFAULT 0,
    sha256     text NOT NULL DEFAULT '',
    blob_ref   text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz
);

CREATE TABLE cache_entries (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id   uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    key          text NOT NULL,
    version      text NOT NULL DEFAULT '',
    scope        text NOT NULL DEFAULT 'trusted'
                 CHECK (scope IN ('trusted', 'pull_request')),
    size_bytes   bigint NOT NULL DEFAULT 0,
    blob_ref     text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz,
    UNIQUE (project_id, key, version, scope)
);

CREATE TABLE environments (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id         uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    name               text NOT NULL,
    protected          boolean NOT NULL DEFAULT false,
    required_approvals integer NOT NULL DEFAULT 0,
    created_at         timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project_id, name)
);

CREATE TABLE secrets (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id         uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    project_id     uuid REFERENCES projects (id) ON DELETE CASCADE,
    environment_id uuid REFERENCES environments (id) ON DELETE CASCADE,
    name           text NOT NULL,
    ciphertext     bytea NOT NULL,
    nonce          bytea NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);
-- NULL project_id means an organization-level secret; a plain UNIQUE constraint would not
-- deduplicate those because PostgreSQL treats NULLs as distinct.
CREATE UNIQUE INDEX secrets_org_unique ON secrets (org_id, name)
    WHERE project_id IS NULL AND environment_id IS NULL;
CREATE UNIQUE INDEX secrets_project_unique ON secrets (project_id, name)
    WHERE project_id IS NOT NULL AND environment_id IS NULL;

CREATE TABLE deployments (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id     uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    environment_id uuid NOT NULL REFERENCES environments (id) ON DELETE CASCADE,
    run_id         uuid REFERENCES workflow_runs (id) ON DELETE SET NULL,
    job_id         uuid REFERENCES jobs (id) ON DELETE SET NULL,
    ref            text NOT NULL DEFAULT '',
    sha            text NOT NULL DEFAULT '',
    status         text NOT NULL DEFAULT 'queued'
                   CHECK (status IN ('queued', 'in_progress', 'success', 'failure', 'rolled_back')),
    url            text NOT NULL DEFAULT '',
    metadata       jsonb NOT NULL DEFAULT '{}',
    created_at     timestamptz NOT NULL DEFAULT now(),
    completed_at   timestamptz
);

CREATE TABLE releases (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    tag        text NOT NULL,
    name       text NOT NULL DEFAULT '',
    body       text NOT NULL DEFAULT '',
    draft      boolean NOT NULL DEFAULT false,
    prerelease boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project_id, tag)
);

CREATE TABLE audit_events (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id        uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    actor_user_id uuid REFERENCES users (id) ON DELETE SET NULL,
    action        text NOT NULL,
    target        text NOT NULL DEFAULT '',
    metadata      jsonb NOT NULL DEFAULT '{}',
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_events_org_created_idx ON audit_events (org_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS audit_events;
DROP TABLE IF EXISTS releases;
DROP TABLE IF EXISTS deployments;
DROP TABLE IF EXISTS secrets;
DROP TABLE IF EXISTS environments;
DROP TABLE IF EXISTS cache_entries;
DROP TABLE IF EXISTS artifacts;
DROP TABLE IF EXISTS job_leases;
ALTER TABLE jobs DROP CONSTRAINT IF EXISTS jobs_runner_fk;
DROP TABLE IF EXISTS runners;
DROP TABLE IF EXISTS steps;
DROP TABLE IF EXISTS jobs;
DROP TABLE IF EXISTS workflow_runs;
DROP TABLE IF EXISTS workflow_versions;
DROP TABLE IF EXISTS workflows;
DROP TABLE IF EXISTS webhook_deliveries;
DROP TABLE IF EXISTS installations;
DROP TABLE IF EXISTS projects;
DROP TABLE IF EXISTS api_tokens;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS org_members;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS orgs;
