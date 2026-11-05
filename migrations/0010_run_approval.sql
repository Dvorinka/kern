-- +goose Up
-- Runs can be held for operator approval (fork-PR gate) before they enter
-- the queued → in_progress pipeline.
ALTER TABLE workflow_runs DROP CONSTRAINT workflow_runs_status_check;
ALTER TABLE workflow_runs ADD CONSTRAINT workflow_runs_status_check
    CHECK (status IN ('awaiting_approval', 'queued', 'in_progress', 'completed', 'cancelled'));

-- +goose Down
ALTER TABLE workflow_runs DROP CONSTRAINT workflow_runs_status_check;
ALTER TABLE workflow_runs ADD CONSTRAINT workflow_runs_status_check
    CHECK (status IN ('queued', 'in_progress', 'completed', 'cancelled'));
