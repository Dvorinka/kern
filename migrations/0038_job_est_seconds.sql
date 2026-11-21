-- +goose Up
-- Historical duration estimate per job (#372): CreateRun sums the
-- workflow's StepMedians across the job's steps, and ClaimJob orders
-- equally-prioritized jobs longest-pole-first so slow lanes start
-- before the queue drains into a long tail.
ALTER TABLE jobs ADD COLUMN est_seconds real NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE jobs DROP COLUMN est_seconds;
