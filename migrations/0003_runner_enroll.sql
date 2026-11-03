-- +goose Up
-- One-time runner enrollment tokens. An operator mints a token via the API
-- (kern enroll / POST /runners/enroll-tokens); the join flow on the target host
-- exchanges it for a per-runner credential at /runners/register. Single use,
-- expiring; the shared static join token remains as a dev escape hatch.
CREATE TABLE runner_enroll_tokens (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id     uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    name       text NOT NULL DEFAULT '',
    token_hash text NOT NULL UNIQUE,
    created_by uuid REFERENCES users (id) ON DELETE SET NULL,
    expires_at timestamptz NOT NULL,
    used_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX runner_enroll_tokens_org ON runner_enroll_tokens (org_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS runner_enroll_tokens;
