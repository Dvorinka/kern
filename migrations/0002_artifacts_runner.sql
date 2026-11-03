-- +goose Up
-- cancel-in-progress semantics for concurrency groups.
ALTER TABLE workflow_runs ADD COLUMN cancel_in_progress boolean NOT NULL DEFAULT false;

-- Re-registration replaces the runner row instead of accumulating stale entries.
-- Drop older duplicate (org, name) rows first so the unique index can build.
DELETE FROM runners a
USING runners b
WHERE a.org_id = b.org_id AND a.name = b.name AND a.created_at < b.created_at;
CREATE UNIQUE INDEX runners_org_name_unique ON runners (org_id, name);

-- GitHub artifact names are unique per run (v4 semantics).
CREATE UNIQUE INDEX artifacts_run_name_unique ON artifacts (run_id, name);

-- +goose Down
DROP INDEX IF EXISTS artifacts_run_name_unique;
DROP INDEX IF EXISTS runners_org_name_unique;
ALTER TABLE workflow_runs DROP COLUMN IF EXISTS cancel_in_progress;
