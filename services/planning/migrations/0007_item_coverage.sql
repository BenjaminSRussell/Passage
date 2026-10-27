-- ItemCoverage (data model 4.22.4 with the FR-22.16..FR-22.20 fields of the
-- PlanningStrategy addendum): how one item is planned at a site, in a
-- warehouse of its own (MRP area) or at a subcontractor. The coverage group is
-- referenced by id inside the tenant. version is the ETag every update bumps;
-- idempotency_key and input_hash let a retried create replay instead of
-- answering 409. Quantities are numeric, never float.
CREATE TABLE IF NOT EXISTS item_coverage (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  item_id uuid NOT NULL,
  site_id uuid NOT NULL,
  warehouse_id uuid,
  coverage_group_id uuid NOT NULL,
  min_qty numeric(18,6) NOT NULL DEFAULT 0 CHECK (min_qty >= 0),
  max_qty numeric(18,6) NOT NULL DEFAULT 0 CHECK (max_qty >= 0),
  safety_margin_receipt_days integer NOT NULL DEFAULT 0 CHECK (safety_margin_receipt_days BETWEEN 0 AND 365),
  lead_time_days integer CHECK (lead_time_days BETWEEN 0 AND 3650),
  vendor_id uuid,
  planned_order_type text NOT NULL DEFAULT 'purchase' CHECK (planned_order_type IN ('purchase', 'production', 'transfer')),
  planning_strategy_code text NOT NULL DEFAULT 'mts-net' CHECK (planning_strategy_code IN
    ('mts-net', 'mts-gross', 'mto', 'assemble-to-order-final', 'plan-without-final-assembly', 'subassembly')),
  planning_scope text NOT NULL DEFAULT 'site' CHECK (planning_scope IN ('site', 'warehouse', 'subcontractor')),
  decoupling_point boolean NOT NULL DEFAULT false,
  buffer_profile_id uuid,
  required_shelf_life_days integer NOT NULL DEFAULT 0 CHECK (required_shelf_life_days BETWEEN 0 AND 3650),
  priority_based boolean NOT NULL DEFAULT false,
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  idempotency_key text,
  input_hash text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CONSTRAINT item_coverage_group_fkey FOREIGN KEY (tenant_id, coverage_group_id) REFERENCES coverage_group (tenant_id, id),
  CONSTRAINT item_coverage_idempotency_key UNIQUE (tenant_id, idempotency_key),
  CHECK (max_qty = 0 OR max_qty >= min_qty),
  -- FR-22.18: a warehouse scope names its warehouse, the other scopes none; a
  -- subcontractor scope names the vendor and plans provision transfers.
  CHECK ((planning_scope = 'warehouse') = (warehouse_id IS NOT NULL)),
  CHECK (planning_scope <> 'subcontractor' OR (vendor_id IS NOT NULL AND planned_order_type = 'transfer')),
  CHECK ((idempotency_key IS NULL) = (input_hash IS NULL))
);
-- One coverage per item, site and planning scope: per warehouse for a
-- warehouse scope, per vendor for a subcontractor scope.
CREATE UNIQUE INDEX IF NOT EXISTS item_coverage_scope_key ON item_coverage (
  tenant_id, item_id, site_id, planning_scope,
  coalesce(warehouse_id, '00000000-0000-0000-0000-000000000000'::uuid),
  (CASE WHEN planning_scope = 'subcontractor' THEN vendor_id ELSE '00000000-0000-0000-0000-000000000000'::uuid END));
CREATE INDEX IF NOT EXISTS item_coverage_group_ix ON item_coverage (tenant_id, coverage_group_id);
ALTER TABLE item_coverage ENABLE ROW LEVEL SECURITY;
ALTER TABLE item_coverage FORCE ROW LEVEL SECURITY;
CREATE POLICY item_coverage_tenant_access ON item_coverage AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
