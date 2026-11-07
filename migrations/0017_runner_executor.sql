-- +goose Up
-- Execution substrate per runner: 'docker' runs job steps inside ephemeral
-- containers; 'host' runs them as local processes on the runner host
-- (bare-metal/macOS runners). Host execution has no isolation, so the claim
-- query refuses fork/pull_request trust tiers for host runners.
ALTER TABLE runners
    ADD COLUMN executor text NOT NULL DEFAULT 'docker'
    CHECK (executor IN ('docker', 'host'));

-- +goose Down
ALTER TABLE runners DROP COLUMN executor;
