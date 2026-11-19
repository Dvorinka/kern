-- +goose Up
-- Project and runner-label grants on API tokens (#356).
-- '{}' means unrestricted, which is every token minted before this.
ALTER TABLE api_tokens ADD COLUMN projects text[] NOT NULL DEFAULT '{}';
ALTER TABLE api_tokens ADD COLUMN labels text[] NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE api_tokens DROP COLUMN labels;
ALTER TABLE api_tokens DROP COLUMN projects;
