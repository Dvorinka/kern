-- +goose Up
-- Live claim-gate observation: the runner reports which claim gates are
-- engaged (disk pressure, memory pressure, load) on every heartbeat so a
-- paused runner stops reading as merely idle. '' = claiming normally;
-- comma-joined reasons otherwise (disk,memory,overload).
ALTER TABLE runners ADD COLUMN claims_paused text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE runners DROP COLUMN claims_paused;
