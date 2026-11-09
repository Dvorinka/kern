-- +goose Up
-- Live-job exec channel (#132): a user queues a command against an
-- in_progress job; the leasing runner picks it up on heartbeat, runs it
-- inside the job container via docker exec, and streams output back.
-- Output/stdin bytes live in the blob store (exec/<job>/<exec>.out|stdin);
-- this table is the state machine only.
CREATE TABLE exec_requests (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id       uuid NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    command      text NOT NULL,
    interactive  boolean NOT NULL DEFAULT false,
    state        text NOT NULL DEFAULT 'pending',
    error        text NOT NULL DEFAULT '',
    exit_code    integer,
    stdin_closed boolean NOT NULL DEFAULT false,
    created_by   uuid,
    created_at   timestamptz NOT NULL DEFAULT now(),
    started_at   timestamptz,
    finished_at  timestamptz
);
CREATE INDEX exec_requests_active ON exec_requests (job_id) WHERE state IN ('pending', 'running');

-- +goose Down
DROP TABLE exec_requests;
