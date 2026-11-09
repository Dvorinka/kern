-- +goose Up
-- Latch for the control-plane disk-pressure alert: set when a heartbeat's
-- free-space sample crosses below the org's disk_alert_pct threshold and
-- cleared when free space recovers past it (with headroom). One audit
-- event per episode instead of one per heartbeat.
ALTER TABLE runners ADD COLUMN disk_alerted boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE runners DROP COLUMN disk_alerted;
