-- BR-9.1 for routings: a draft route version can be cancelled. The row, its operations and
-- requirements are kept so the draft stays auditable; cancelled is terminal and the active-route
-- lookups only read active versions.
ALTER TABLE route_version DROP CONSTRAINT IF EXISTS route_version_status_check;
ALTER TABLE route_version ADD CONSTRAINT route_version_status_check
  CHECK (status IN ('draft', 'active', 'expired', 'cancelled'));

-- FR-9.12 copy lineage: a version created by :copy records the version it was copied from. The
-- source stays in the same tenant (composite foreign key) and is never deleted, so the link holds.
-- No new table, so no grants change (passage_app already has SELECT, INSERT and UPDATE).
ALTER TABLE bom_version ADD COLUMN IF NOT EXISTS copied_from_version_id uuid;
ALTER TABLE route_version ADD COLUMN IF NOT EXISTS copied_from_version_id uuid;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'bom_version_copied_from_fk' AND conrelid = 'bom_version'::regclass) THEN
    ALTER TABLE bom_version ADD CONSTRAINT bom_version_copied_from_fk
      FOREIGN KEY (tenant_id, copied_from_version_id) REFERENCES bom_version (tenant_id, id);
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'route_version_copied_from_fk' AND conrelid = 'route_version'::regclass) THEN
    ALTER TABLE route_version ADD CONSTRAINT route_version_copied_from_fk
      FOREIGN KEY (tenant_id, copied_from_version_id) REFERENCES route_version (tenant_id, id);
  END IF;
END
$$;
