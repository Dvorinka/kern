-- +goose Up
-- First-class failure classification, rolled up from the failed steps'
-- diagnosis kinds (or a jobErrorHint-derived class for prepare-phase
-- failures that never reach a step). '' = no classification recorded.
ALTER TABLE jobs ADD COLUMN classification text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE jobs DROP COLUMN classification;
