-- +goose Up
-- Requeue backoff (#373): transient retries wait a jittered exponential
-- delay instead of hot-looping onto the same fleet.

ALTER TABLE jobs ADD COLUMN IF NOT EXISTS not_before timestamptz;

-- The claim query only needs "due" rows; a partial index keeps the waiters
-- out of the hot path.
CREATE INDEX IF NOT EXISTS jobs_queued_not_before ON jobs (not_before)
    WHERE status = 'queued' AND not_before IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS jobs_queued_not_before;
ALTER TABLE jobs DROP COLUMN IF EXISTS not_before;
