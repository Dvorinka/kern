-- +goose Up
-- Environments + deployment approvals: jobs can name a protected environment;
-- protected jobs park in waiting_approval until enough distinct users approve
-- the deployment row (environments.required_approvals).

ALTER TABLE jobs
    DROP CONSTRAINT jobs_status_check,
    ADD CONSTRAINT jobs_status_check CHECK (status IN (
        'queued', 'waiting', 'waiting_approval', 'in_progress',
        'completed', 'cancelled', 'skipped')),
    ADD COLUMN environment     text NOT NULL DEFAULT '',
    ADD COLUMN environment_url text NOT NULL DEFAULT '';

-- approvals: [{user_id, email, at}] recorded on the deployment; the job
-- unblocks once len(approvals) >= environments.required_approvals.
ALTER TABLE deployments
    ADD COLUMN approvals jsonb NOT NULL DEFAULT '[]';

CREATE INDEX deployments_project_idx ON deployments (project_id, created_at DESC);
CREATE INDEX deployments_job_idx ON deployments (job_id);

-- Environment-scoped secrets get their own uniqueness domain, matching the
-- org- and project-scoped partial indexes from 0001.
CREATE UNIQUE INDEX secrets_env_unique ON secrets (environment_id, name)
    WHERE environment_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS secrets_env_unique;
ALTER TABLE deployments DROP COLUMN approvals;
DROP INDEX IF EXISTS deployments_job_idx;
DROP INDEX IF EXISTS deployments_project_idx;
ALTER TABLE jobs
    DROP COLUMN environment_url,
    DROP COLUMN environment,
    DROP CONSTRAINT jobs_status_check,
    ADD CONSTRAINT jobs_status_check CHECK (status IN (
        'queued', 'waiting', 'in_progress', 'completed', 'cancelled', 'skipped'));
