-- +goose Up
-- Protection rules beyond a bare approval count: a named reviewer list,
-- a wait before the job may start, and which branches may deploy.
-- shortcut: no branch-protection source, so the policy is all or selected
-- name patterns. * does not match /.

ALTER TABLE environments
    ADD COLUMN reviewers       text[] NOT NULL DEFAULT '{}',
    ADD COLUMN wait_minutes    integer NOT NULL DEFAULT 0,
    ADD COLUMN branch_policy   text NOT NULL DEFAULT 'all',
    ADD COLUMN branch_patterns text[] NOT NULL DEFAULT '{}',
    ADD CONSTRAINT environments_wait_minutes_check
        CHECK (wait_minutes >= 0 AND wait_minutes <= 43200),
    ADD CONSTRAINT environments_branch_policy_check
        CHECK (branch_policy IN ('all', 'selected'));

ALTER TABLE deployments
    ADD COLUMN wait_until timestamptz;

-- +goose Down
ALTER TABLE deployments DROP COLUMN wait_until;
ALTER TABLE environments
    DROP CONSTRAINT environments_branch_policy_check,
    DROP CONSTRAINT environments_wait_minutes_check,
    DROP COLUMN branch_patterns,
    DROP COLUMN branch_policy,
    DROP COLUMN wait_minutes,
    DROP COLUMN reviewers;
