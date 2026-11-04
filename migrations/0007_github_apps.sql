-- +goose Up
-- Per-org GitHub Apps created through the manifest ("Connect GitHub") flow.
-- pem + webhook_secret are AES-256-GCM ciphertext (secrets.Cipher.Encode).
CREATE TABLE github_apps (
    id                       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                   uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    app_id                   bigint NOT NULL,
    slug                     text NOT NULL,
    name                     text NOT NULL DEFAULT '',
    pem_ciphertext           bytea NOT NULL,
    pem_nonce                bytea NOT NULL,
    secret_ciphertext        bytea NOT NULL DEFAULT ''::bytea,
    secret_nonce             bytea NOT NULL DEFAULT ''::bytea,
    html_url                 text NOT NULL DEFAULT '',
    created_by               uuid,
    created_at               timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, app_id)
);

-- +goose Down
DROP TABLE IF EXISTS github_apps;
