-- +goose Up
-- Cleared runs stay in the database (audit, diagnostics, direct links) but
-- drop out of default run lists. Set when a cancelled run is re-triggered
-- or when an operator clears the cancelled backlog.
ALTER TABLE workflow_runs ADD COLUMN cleared_at timestamptz;

-- +goose Down
ALTER TABLE workflow_runs DROP COLUMN cleared_at;
