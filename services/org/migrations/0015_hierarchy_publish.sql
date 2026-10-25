ALTER TABLE hierarchy_version ADD COLUMN IF NOT EXISTS published_at timestamptz;
ALTER TABLE hierarchy_version ADD COLUMN IF NOT EXISTS published_by text;
ALTER TABLE hierarchy_version ADD COLUMN IF NOT EXISTS publish_reason text CHECK (publish_reason IS NULL OR char_length(publish_reason) BETWEEN 10 AND 500);
ALTER TABLE hierarchy_version ADD COLUMN IF NOT EXISTS publish_idempotency_key text;
CREATE UNIQUE INDEX IF NOT EXISTS hierarchy_version_publish_idempotency_key ON hierarchy_version (tenant_id, publish_idempotency_key);
CREATE INDEX IF NOT EXISTS hierarchy_version_as_of ON hierarchy_version (tenant_id, hierarchy_type_id, effective_from) WHERE status <> 'draft';

CREATE TABLE IF NOT EXISTS hierarchy_closure (
  tenant_id uuid NOT NULL,
  version_id uuid NOT NULL,
  ancestor_org_unit_id uuid NOT NULL,
  descendant_org_unit_id uuid NOT NULL,
  depth integer NOT NULL CHECK (depth >= 1),
  PRIMARY KEY (tenant_id, version_id, ancestor_org_unit_id, descendant_org_unit_id),
  FOREIGN KEY (tenant_id, version_id) REFERENCES hierarchy_version (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS hierarchy_closure_descendant ON hierarchy_closure (tenant_id, version_id, descendant_org_unit_id, depth);
ALTER TABLE hierarchy_closure ENABLE ROW LEVEL SECURITY;
ALTER TABLE hierarchy_closure FORCE ROW LEVEL SECURITY;
CREATE POLICY hierarchy_closure_tenant_access ON hierarchy_closure AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
