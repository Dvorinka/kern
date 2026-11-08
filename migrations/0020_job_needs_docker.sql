-- +goose Up
-- Jobs whose steps invoke the docker CLI or docker/* actions prefer
-- kern-docker-advertising runners (host executors with a working daemon)
-- at claim time; the flag is a scheduling hint, not a hard requirement.
ALTER TABLE jobs ADD COLUMN needs_docker boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE jobs DROP COLUMN needs_docker;
