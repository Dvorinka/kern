-- +goose Up
-- Whether an external scaler (deployments/vm-scaler.sh) may power this
-- runner's host on demand for queued work. Default true preserves current
-- behavior; operators opt runners out per row.
ALTER TABLE runners ADD COLUMN wake_on_demand BOOLEAN NOT NULL DEFAULT true;

-- +goose Down
ALTER TABLE runners DROP COLUMN wake_on_demand;
