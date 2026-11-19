-- +goose Up
-- Per-token job concurrency cap (#356) and the dispatching token on a run,
-- so the claim query can hold a tenant to its slot budget.
ALTER TABLE api_tokens ADD COLUMN max_concurrent_jobs int NOT NULL DEFAULT 0;
ALTER TABLE workflow_runs ADD COLUMN dispatch_token_id uuid;
-- The claim query counts each capped token's in-progress jobs through
-- this column; only token-dispatched runs carry it.
CREATE INDEX workflow_runs_dispatch_token_idx
    ON workflow_runs (dispatch_token_id) WHERE dispatch_token_id IS NOT NULL;

-- +goose Down
DROP INDEX workflow_runs_dispatch_token_idx;
ALTER TABLE workflow_runs DROP COLUMN dispatch_token_id;
ALTER TABLE api_tokens DROP COLUMN max_concurrent_jobs;
