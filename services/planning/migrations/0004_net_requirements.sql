-- FR-22.12, AC-22.10: the time-phased net requirements (MD04) view of every
-- item of a plan version, one row per item and day with movement, written by
-- the run in its transaction and purged with the version.
CREATE TABLE IF NOT EXISTS net_requirement (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  plan_version_id uuid NOT NULL,
  item_id uuid NOT NULL,
  day date NOT NULL,
  gross_requirements numeric(18,6) NOT NULL CHECK (gross_requirements >= 0),
  scheduled_receipts numeric(18,6) NOT NULL CHECK (scheduled_receipts >= 0),
  planned_receipts numeric(18,6) NOT NULL CHECK (planned_receipts >= 0),
  pab numeric(18,6) NOT NULL,
  net_requirement numeric(18,6) NOT NULL CHECK (net_requirement >= 0),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, plan_version_id, item_id, day),
  FOREIGN KEY (tenant_id, plan_version_id) REFERENCES plan_version (tenant_id, id)
);
ALTER TABLE net_requirement ENABLE ROW LEVEL SECURITY;
ALTER TABLE net_requirement FORCE ROW LEVEL SECURITY;
CREATE POLICY net_requirement_tenant_access ON net_requirement AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
