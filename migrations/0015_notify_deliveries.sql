-- +goose Up
-- Persistent outbound notification deliveries: a finished run enqueues one
-- row per matching endpoint; the leader-gated tick retries due rows with
-- backoff so a restarted api does not lose notifications mid-flight.
CREATE TABLE notify_deliveries (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id         uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    project_id     uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    run_id         uuid NOT NULL REFERENCES workflow_runs (id) ON DELETE CASCADE,
    url            text NOT NULL,
    payload        jsonb NOT NULL,
    attempts       int NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    last_error     text NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now(),
    delivered_at   timestamptz
);
CREATE INDEX notify_deliveries_due_idx ON notify_deliveries (next_attempt_at)
    WHERE delivered_at IS NULL;

-- +goose Down
DROP TABLE IF EXISTS notify_deliveries;
