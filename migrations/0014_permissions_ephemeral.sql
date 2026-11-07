-- +goose Up
-- The runners.ephemeral column existed since 0001 but defaulted true and was
-- never written — every pre-existing row is a persistent fleet member, so
-- backfill false. New rows set the flag explicitly at insert.
UPDATE runners SET ephemeral = false WHERE ephemeral = true;

-- Per-job GitHub Actions `permissions:` ceiling, carried from the compiled
-- plan so the control plane can scope the per-run GITHUB_TOKEN it mints.
ALTER TABLE jobs ADD COLUMN permissions jsonb NOT NULL DEFAULT '{}'::jsonb;

-- +goose Down
ALTER TABLE jobs DROP COLUMN IF EXISTS permissions;
UPDATE runners SET ephemeral = true WHERE ephemeral = false;
