-- +goose Up
-- Project configuration variables, surfaced to workflows as the `vars`
-- expression context (GitHub Actions parity). Values are plaintext by
-- design: vars are configuration, not credentials, and are delivered to
-- every run including untrusted pull_request runs — unlike secrets.
CREATE TABLE project_vars (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    name       text NOT NULL,
    value      text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project_id, name)
);

-- +goose Down
DROP TABLE project_vars;
