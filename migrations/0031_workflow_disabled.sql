-- +goose Up
-- Per-workflow disable switch: a disabled workflow keeps its synced versions
-- and history but no trigger (webhook, schedule, dispatch, rerun) can start
-- a run. Used to keep GitHub-Actions-owned pipelines (e.g. release.yml) from
-- double-firing on KERN, and to retire workflows deleted from the repo.
ALTER TABLE workflows ADD COLUMN disabled boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE workflows DROP COLUMN disabled;
