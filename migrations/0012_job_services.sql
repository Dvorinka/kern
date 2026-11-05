-- +goose Up
-- Job service containers (jobs.<id>.services): the compiled service specs
-- travel with the job so the runner can start them on the job network.
ALTER TABLE jobs ADD COLUMN services jsonb NOT NULL DEFAULT '[]'::jsonb;

-- +goose Down
ALTER TABLE jobs DROP COLUMN services;
