-- +goose Up
-- Tenant isolation for webhook deliveries (#368): deliveries are written
-- before the repo is resolved, so org_id starts NULL and is filled once the
-- payload's repository maps to a project (or the verifying app's org is
-- known). Rows that never resolve stay invisible to every org's delivery
-- views rather than leaking another tenant's payloads.
ALTER TABLE webhook_deliveries ADD COLUMN org_id uuid REFERENCES orgs(id) ON DELETE CASCADE;
CREATE INDEX webhook_deliveries_org_idx ON webhook_deliveries (org_id, received_at DESC) WHERE org_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS webhook_deliveries_org_idx;
ALTER TABLE webhook_deliveries DROP COLUMN IF EXISTS org_id;
