-- Wave 21 slice 22: "End jobs on last RAF" (PRD 4.21.13, FR-21.7) and the daily order cost
-- snapshot (PRD 4.21.16 `order-cost-snapshot`).
-- Additive (expand): defaulted columns and one new table; rows written before this migration
-- keep the behaviour they had apart from the PRD default of the new parameter (on).

-- The site switch, on by default as in the PRD; the flags remember which jobs and operations
-- the last report as finished closed, so a reversal reopens exactly those.
ALTER TABLE production_parameters ADD COLUMN IF NOT EXISTS end_jobs boolean NOT NULL DEFAULT true;
ALTER TABLE production_job ADD COLUMN IF NOT EXISTS ended_by_raf boolean NOT NULL DEFAULT false;
ALTER TABLE production_operation ADD COLUMN IF NOT EXISTS ended_by_raf boolean NOT NULL DEFAULT false;

-- One row per open order and day: planned cost against actual cost and the WIP balance, for the
-- trend of the order KPIs. A second run on the same day replaces the day's row (upsert).
CREATE TABLE IF NOT EXISTS production_cost_snapshot (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  order_id uuid NOT NULL,
  snapshot_date date NOT NULL,
  order_number text NOT NULL,
  site_id uuid NOT NULL,
  status text NOT NULL,
  currency char(3) NOT NULL,
  qty_ordered numeric(18,6) NOT NULL,
  qty_reported numeric(18,6) NOT NULL,
  planned_cost numeric(18,4) NOT NULL,
  actual_cost numeric(18,4) NOT NULL,
  wip_balance numeric(18,4) NOT NULL,
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, order_id, snapshot_date)
);
CREATE INDEX IF NOT EXISTS production_cost_snapshot_date ON production_cost_snapshot (tenant_id, legal_entity_id, snapshot_date DESC);

ALTER TABLE production_cost_snapshot ENABLE ROW LEVEL SECURITY;
ALTER TABLE production_cost_snapshot FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS production_cost_snapshot_tenant_access ON production_cost_snapshot;
CREATE POLICY production_cost_snapshot_tenant_access ON production_cost_snapshot AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
DROP POLICY IF EXISTS production_cost_snapshot_legal_entity_scope ON production_cost_snapshot;
CREATE POLICY production_cost_snapshot_legal_entity_scope ON production_cost_snapshot AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- The store inserts, and replaces the day's row on a second run (upsert), never deletes.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON production_cost_snapshot TO passage_app';
  END IF;
END
$$;
