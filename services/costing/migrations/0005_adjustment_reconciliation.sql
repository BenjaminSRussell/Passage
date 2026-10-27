-- Wave 20 slice 6: manual cost adjustments (FR-20.6) and stored inventory-to-GL
-- reconciliation runs that emit reconciliation.diff (FR-20.4). Additive only.

-- One manual adjustment. ON_HAND revalues the stock of a product x site x
-- warehouse x valuation type at a date; RECEIPT changes the value of one receipt
-- transaction by an amount. Its value effect is the MANUAL_ADJ cost transaction
-- (source key manual-adjustment:<id>, cost_transaction_id) written in the same
-- transaction, so the value report moves at once. Adjustments are append-only:
-- a correction is a further adjustment. valuation_ledger_id NULL is the primary
-- valuation, as in cost_transaction. The ledger voucher (inventory / cost
-- adjustment account) waits for the wave 11-14 posting client.
CREATE TABLE IF NOT EXISTS cost_adjustment (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  kind text NOT NULL CHECK (kind IN ('ON_HAND', 'RECEIPT')),
  mode text NOT NULL CHECK (mode IN ('UNIT_COST', 'AMOUNT')),
  product_id uuid NOT NULL,
  site_id uuid NOT NULL,
  warehouse_id uuid,
  valuation_type text NOT NULL DEFAULT '' CHECK (char_length(valuation_type) <= 64),
  valuation_ledger_id uuid,
  receipt_transaction_id uuid,
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  financial_date date NOT NULL,
  reason text NOT NULL CHECK (char_length(reason) BETWEEN 1 AND 500),
  unit_cost numeric(19,6) CHECK (unit_cost > 0),
  amount numeric(19,4),
  qty numeric(28,6) NOT NULL,
  old_value numeric(19,4) NOT NULL,
  new_value numeric(19,4) NOT NULL,
  delta numeric(19,4) NOT NULL CHECK (delta <> 0),
  old_unit_cost numeric(19,6),
  new_unit_cost numeric(19,6),
  cost_transaction_id uuid NOT NULL,
  idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 255),
  request_hash text NOT NULL CHECK (char_length(request_hash) = 64),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CONSTRAINT cost_adjustment_idempotency UNIQUE (tenant_id, idempotency_key),
  CHECK ((kind = 'RECEIPT') = (receipt_transaction_id IS NOT NULL)),
  CHECK (kind = 'ON_HAND' OR mode = 'AMOUNT'),
  CHECK ((mode = 'UNIT_COST') = (unit_cost IS NOT NULL)),
  CHECK ((mode = 'AMOUNT') = (amount IS NOT NULL)),
  CHECK (new_value - old_value = delta)
);
CREATE INDEX IF NOT EXISTS cost_adjustment_list ON cost_adjustment (tenant_id, legal_entity_id, financial_date DESC, id DESC);
CREATE INDEX IF NOT EXISTS cost_adjustment_product ON cost_adjustment (tenant_id, product_id, site_id);

CREATE OR REPLACE FUNCTION cost_adjustment_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'cost_adjustment is append-only' USING ERRCODE = 'P0001';
END;
$$;
DROP TRIGGER IF EXISTS cost_adjustment_no_update ON cost_adjustment;
CREATE TRIGGER cost_adjustment_no_update BEFORE UPDATE OR DELETE ON cost_adjustment
  FOR EACH ROW EXECUTE FUNCTION cost_adjustment_append_only();

ALTER TABLE cost_adjustment ENABLE ROW LEVEL SECURITY;
ALTER TABLE cost_adjustment FORCE ROW LEVEL SECURITY;
CREATE POLICY cost_adjustment_tenant_access ON cost_adjustment AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY cost_adjustment_legal_entity_scope ON cost_adjustment AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- One stored tie-out of the value report against ledger balances (the run of
-- costing.reconcile, or a manual one). body is the run as served, rows
-- included, so a read returns exactly what was computed; every differing row
-- also wrote one reconciliation.diff event to the outbox in the same
-- transaction. request_hash guards Idempotency-Key reuse.
CREATE TABLE IF NOT EXISTS reconciliation_run (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  valuation_ledger_id uuid,
  as_of date NOT NULL,
  ties boolean NOT NULL,
  differences integer NOT NULL CHECK (differences >= 0),
  idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 255),
  request_hash text NOT NULL CHECK (char_length(request_hash) = 64),
  body jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CONSTRAINT reconciliation_run_idempotency UNIQUE (tenant_id, idempotency_key),
  CHECK (ties = (differences = 0))
);
CREATE INDEX IF NOT EXISTS reconciliation_run_list ON reconciliation_run (tenant_id, legal_entity_id, as_of DESC, id DESC);

CREATE OR REPLACE FUNCTION reconciliation_run_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'reconciliation_run is append-only' USING ERRCODE = 'P0001';
END;
$$;
DROP TRIGGER IF EXISTS reconciliation_run_no_update ON reconciliation_run;
CREATE TRIGGER reconciliation_run_no_update BEFORE UPDATE OR DELETE ON reconciliation_run
  FOR EACH ROW EXECUTE FUNCTION reconciliation_run_append_only();

ALTER TABLE reconciliation_run ENABLE ROW LEVEL SECURITY;
ALTER TABLE reconciliation_run FORCE ROW LEVEL SECURITY;
CREATE POLICY reconciliation_run_tenant_access ON reconciliation_run AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY reconciliation_run_legal_entity_scope ON reconciliation_run AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
