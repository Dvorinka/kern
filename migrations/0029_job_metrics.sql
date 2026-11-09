-- +goose Up
-- Per-job container resource peaks (docker stats sampled during the job):
-- {"cpu_pct_peak": 87.5, "mem_bytes_peak": 734003200}. Host executors leave it empty.
ALTER TABLE jobs ADD COLUMN metrics jsonb NOT NULL DEFAULT '{}'::jsonb;

-- +goose Down
ALTER TABLE jobs DROP COLUMN metrics;
