-- +goose Up
-- W5 self-heal + diagnostics. Every failed step carries a structured diagnosis;
-- org/project settings drive the remediation ladder (self_heal: off|auto|approve,
-- project value overrides org, missing = off).
ALTER TABLE steps ADD COLUMN diagnosis jsonb;
ALTER TABLE orgs ADD COLUMN settings jsonb NOT NULL DEFAULT '{}';
ALTER TABLE projects ADD COLUMN settings jsonb NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE steps DROP COLUMN IF EXISTS diagnosis;
ALTER TABLE orgs DROP COLUMN IF EXISTS settings;
ALTER TABLE projects DROP COLUMN IF EXISTS settings;
