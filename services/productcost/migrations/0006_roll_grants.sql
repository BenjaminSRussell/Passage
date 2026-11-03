ALTER TABLE cost_calculation ALTER COLUMN bom_version_id DROP NOT NULL;
ALTER TABLE cost_calculation ALTER COLUMN route_version_id DROP NOT NULL;
ALTER TABLE cost_calculation ADD COLUMN IF NOT EXISTS rolled_by text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS cost_calculation_one_pending
  ON cost_calculation (tenant_id, costing_version_id, item_id, site_id, valuation_view)
  WHERE status = 'Pending';
ALTER TABLE standard_cost_activation_attempt ADD COLUMN IF NOT EXISTS ledger_idempotency_ttl_days integer NOT NULL DEFAULT 400;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT INSERT ON cost_calculation TO passage_app';
    EXECUTE 'GRANT UPDATE (status) ON cost_calculation TO passage_app';
  END IF;
END
$$;
