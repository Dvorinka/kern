-- +goose Up
-- Workflow-command annotations (::error::/::warning::/::notice::/::debug::)
-- collected by the runner per step. JSON array; empty for steps that emit
-- none. Written once at step finish alongside diagnosis.
ALTER TABLE steps ADD COLUMN annotations jsonb NOT NULL DEFAULT '[]'::jsonb;

-- +goose Down
ALTER TABLE steps DROP COLUMN IF EXISTS annotations;
