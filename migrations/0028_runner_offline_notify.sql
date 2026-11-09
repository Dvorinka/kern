-- +goose Up
-- Runner-level events (runner.offline) ride the same delivery queue as
-- run.completed but are not tied to a project or run.
ALTER TABLE notify_deliveries ALTER COLUMN project_id DROP NOT NULL;
ALTER TABLE notify_deliveries ALTER COLUMN run_id DROP NOT NULL;
ALTER TABLE notify_deliveries ADD COLUMN event text NOT NULL DEFAULT 'run.completed';

-- +goose Down
ALTER TABLE notify_deliveries DROP COLUMN event;
ALTER TABLE notify_deliveries ALTER COLUMN project_id SET NOT NULL;
ALTER TABLE notify_deliveries ALTER COLUMN run_id SET NOT NULL;
