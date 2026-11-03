-- +goose Up
-- Live utilization reported with each runner heartbeat (W3 fleet metrics).
-- NULL means the runner has not reported yet. disk_free_bytes mirrors the
-- capacity semantics of runners.disk_bytes (available space, not total).
ALTER TABLE runners
    ADD COLUMN cpu_percent    real,
    ADD COLUMN mem_used_bytes bigint,
    ADD COLUMN disk_free_bytes bigint,
    ADD COLUMN metrics_at     timestamptz;

-- +goose Down
ALTER TABLE runners
    DROP COLUMN IF EXISTS cpu_percent,
    DROP COLUMN IF EXISTS mem_used_bytes,
    DROP COLUMN IF EXISTS disk_free_bytes,
    DROP COLUMN IF EXISTS metrics_at;
