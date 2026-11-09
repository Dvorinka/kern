-- +goose Up
-- Operator-managed runner env overrides. Delivered to the runner on each
-- heartbeat and merged into every job's base env server-side, so app-set
-- values win over container/env-file defaults ("app overwrites env").
ALTER TABLE runners ADD COLUMN env jsonb NOT NULL DEFAULT '{}';

-- Desired runner configuration captured at enrollment: the minted token
-- carries {auto_update, env} which registerRunner applies to the runner
-- row when the join exchanges the token.
ALTER TABLE runner_enroll_tokens ADD COLUMN config jsonb;

-- +goose Down
ALTER TABLE runner_enroll_tokens DROP COLUMN config;
ALTER TABLE runners DROP COLUMN env;
