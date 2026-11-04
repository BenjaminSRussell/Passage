-- Fulfillment slice 23: over-delivery tolerance (BR-28.7, FUL-0005). The
-- tolerance is a percentage of the planned quantity a pick may exceed, set per
-- product and customer, per product, per customer or for everyone (section
-- 5.27 overdelivery_pct, default 0). A scope is a row; the most specific one
-- wins: product and customer, then product, then customer, then the general
-- row. Tenant wide, like packing_rule.
CREATE TABLE IF NOT EXISTS delivery_tolerance (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  product_id uuid,
  customer_id uuid,
  overdelivery_pct numeric(7,4) NOT NULL CHECK (overdelivery_pct >= 0 AND overdelivery_pct <= 100),
  version integer NOT NULL DEFAULT 1,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS delivery_tolerance_scope_uq ON delivery_tolerance (tenant_id,
  coalesce(product_id, '00000000-0000-0000-0000-000000000000'::uuid),
  coalesce(customer_id, '00000000-0000-0000-0000-000000000000'::uuid));

ALTER TABLE delivery_tolerance ENABLE ROW LEVEL SECURITY;
ALTER TABLE delivery_tolerance FORCE ROW LEVEL SECURITY;
CREATE POLICY delivery_tolerance_tenant_access ON delivery_tolerance AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- A pick may exceed the planned quantity by the tolerance in force when it was
-- confirmed. The line keeps that percentage, and the check keeps the picked
-- quantity within it.
ALTER TABLE shipment_line ADD COLUMN IF NOT EXISTS over_tolerance_pct numeric(7,4) NOT NULL DEFAULT 0
  CHECK (over_tolerance_pct >= 0 AND over_tolerance_pct <= 100);
DO $$
DECLARE c record;
BEGIN
  FOR c IN SELECT conname FROM pg_constraint
           WHERE conrelid = 'shipment_line'::regclass AND contype = 'c'
             AND pg_get_constraintdef(oid) LIKE '%qty_picked <= qty_planned%'
             AND conname <> 'shipment_line_picked_within_tolerance'
  LOOP
    EXECUTE format('ALTER TABLE shipment_line DROP CONSTRAINT %I', c.conname);
  END LOOP;
END
$$;
ALTER TABLE shipment_line DROP CONSTRAINT IF EXISTS shipment_line_picked_within_tolerance;
ALTER TABLE shipment_line ADD CONSTRAINT shipment_line_picked_within_tolerance
  CHECK (qty_picked <= qty_planned * (1 + over_tolerance_pct / 100));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON delivery_tolerance TO passage_app';
  END IF;
END
$$;
