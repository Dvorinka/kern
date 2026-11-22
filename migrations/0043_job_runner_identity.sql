-- +goose Up
-- Runner identity stamped at claim time (#413): runner_name/runner_version
-- survive runner deletion and self-updates, so `kern runs show` and
-- `kern diagnose` report who ran the attempt instead of the fleet's
-- current row. config_warnings records referenced-but-undefined
-- secrets.*/vars.* names detected at claim for diagnose surfacing.
ALTER TABLE jobs
    ADD COLUMN runner_name text NOT NULL DEFAULT '',
    ADD COLUMN runner_version text NOT NULL DEFAULT '',
    ADD COLUMN config_warnings text[] NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE jobs
    DROP COLUMN runner_name,
    DROP COLUMN runner_version,
    DROP COLUMN config_warnings;
