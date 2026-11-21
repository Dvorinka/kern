-- +goose Up
-- Fork trust boundary (#368): workflow_versions gains a trusted flag marking
-- versions whose content comes from the canonical (default-branch) workflow
-- definition. Untrusted pull_request heads and ad-hoc dispatches still store
-- versions for run history, but LatestWorkflowVersion and the cron/prime
-- pickers ignore them so PR YAML can never execute as trusted code.
ALTER TABLE workflow_versions ADD COLUMN trusted boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE workflow_versions DROP COLUMN IF EXISTS trusted;
