-- +goose Up
-- Per-job secrets delivery spec for reusable-workflow callers
-- (jobs.<id>.secrets). jsonb 'null' or SQL NULL = deliver the full eligible
-- set (normal jobs and 'secrets: inherit'); '{}' = deliver none; otherwise a
-- map of called-secret-name -> caller-side expression resolved at claim
-- time by the API before the runner ever sees a value.
ALTER TABLE jobs ADD COLUMN secrets_map jsonb;

-- +goose Down
ALTER TABLE jobs DROP COLUMN secrets_map;
