-- +goose Up
-- Per-step GITHUB_STEP_SUMMARY content. The markdown body lives in the blob
-- store (summaries/<job>/<n>); the column is only a presence flag + ref.
ALTER TABLE steps ADD COLUMN summary_ref text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE steps DROP COLUMN IF EXISTS summary_ref;
