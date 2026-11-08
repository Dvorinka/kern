-- +goose Up
-- Per-user UI preferences (theme, etc.). Kept out of `users` so the hot
-- user queries don't carry a jsonb column they never read.
CREATE TABLE user_prefs (
    user_id    uuid PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    prefs      jsonb NOT NULL DEFAULT '{}'::jsonb,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE user_prefs;
