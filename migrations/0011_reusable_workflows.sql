-- +goose Up
-- Reusable workflows: resolved dep contents are pinned on the version so run
-- start recompiles deterministically; jobs carry workflow_call inputs.
ALTER TABLE workflow_versions ADD COLUMN deps jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE jobs ADD COLUMN inputs jsonb NOT NULL DEFAULT '{}'::jsonb;

-- +goose Down
ALTER TABLE jobs DROP COLUMN inputs;
ALTER TABLE workflow_versions DROP COLUMN deps;
