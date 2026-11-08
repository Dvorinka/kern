-- +goose Up
-- Persistent self-update opt-in per runner — settable from the API/CLI/UI so
-- the offer gate doesn't depend solely on the runner's env flag.
ALTER TABLE runners ADD COLUMN auto_update BOOLEAN NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE runners DROP COLUMN auto_update;
