-- Tenant isolation: owner_id becomes a required, indexed column.
-- The project is pre-launch/local, so existing assets (which have a nullable,
-- possibly-NULL owner_id and are unreachable under the new owner-scoped queries)
-- are deleted rather than backfilled. Deleting assets cascades to dependent
-- variants.image / variants.video / jobs (all ON DELETE CASCADE).
DELETE FROM assets;

ALTER TABLE assets ALTER COLUMN owner_id SET NOT NULL;

CREATE INDEX IF NOT EXISTS idx_assets_owner ON assets (owner_id);
