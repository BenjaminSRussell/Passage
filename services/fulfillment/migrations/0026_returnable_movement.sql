-- Fulfillment slice 22: the movements behind a returnable transport packaging balance
-- (FR-28.16, BR-28.13). Every change of a balance writes one row: the containers that left
-- with a packing slip (SHIPPED), came back when that slip was cancelled (SHIPMENT_CANCELLED),
-- a customer's return receipt (RETURNED) and a logistics admin's manual correction
-- (ADJUSTED, with a mandatory reason). qty is signed (plus raises the balance) and
-- balance_after is the balance the same transaction left, so the history of one customer and
-- container type replays to its balance. The table is append-only. Balances that existed
-- before this migration have no history; the first movement shows the balance it moved from.
CREATE TABLE IF NOT EXISTS returnable_packaging_movement (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  customer_id uuid NOT NULL,
  container_type text NOT NULL,
  kind text NOT NULL CHECK (kind IN ('SHIPPED', 'SHIPMENT_CANCELLED', 'RETURNED', 'ADJUSTED')),
  qty numeric(20,6) NOT NULL CHECK (qty <> 0),
  balance_after numeric(20,6) NOT NULL CHECK (balance_after >= 0),
  reference text,
  reason text,
  actor text,
  -- clock_timestamp(), not now(): every writer inserts after locking the balance row, so the
  -- insert time orders the movements as the balance changed even when transactions overlap.
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, container_type) REFERENCES container_type (tenant_id, code),
  CHECK (kind <> 'ADJUSTED' OR (reason IS NOT NULL AND length(btrim(reason)) > 0))
);
CREATE INDEX IF NOT EXISTS returnable_packaging_movement_customer_idx
  ON returnable_packaging_movement (tenant_id, customer_id, container_type, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS returnable_packaging_movement_created_idx
  ON returnable_packaging_movement (tenant_id, created_at DESC, id DESC);

ALTER TABLE returnable_packaging_movement ENABLE ROW LEVEL SECURITY;
ALTER TABLE returnable_packaging_movement FORCE ROW LEVEL SECURITY;
CREATE POLICY returnable_packaging_movement_tenant_access ON returnable_packaging_movement AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT ON returnable_packaging_movement TO passage_app';
  END IF;
END
$$;
