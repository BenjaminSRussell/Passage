-- Wave 20 slice 5: the CostTransaction projection behind the inventory value
-- report and its GL tie-out (FR-20.4, FR-20.15, FR-20.16). Additive only.

-- One row per value-affecting event (3.20.4). Rows are append-only: a correction
-- is a further row, never an update. source_key is the producer's own key for
-- the event (a voucher line, a settlement row), so a repeated feed is a replay.
-- valuation_ledger_id NULL is the legal entity's primary (default) valuation;
-- rows of the primary ledger are stored with NULL, so the primary valuation
-- never splits over two keys. qty is 0 for value-only kinds; PPV rows are kept
-- for the PPV account and never count in the inventory value.
-- inventory_transaction_id and ledger_entry_id are nullable until the wave 16
-- transactions and the wave 11-14 vouchers feed this table (an on-hand
-- revaluation has no inventory transaction).
CREATE TABLE IF NOT EXISTS cost_transaction (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  source_key text NOT NULL CHECK (char_length(source_key) BETWEEN 1 AND 200),
  inventory_transaction_id uuid,
  ledger_entry_id uuid,
  product_id uuid NOT NULL,
  site_id uuid NOT NULL,
  warehouse_id uuid,
  valuation_type text NOT NULL DEFAULT '' CHECK (char_length(valuation_type) <= 64),
  valuation_ledger_id uuid,
  kind text NOT NULL CHECK (kind IN ('PHYSICAL', 'FINANCIAL', 'INVOICE_DIFF', 'SETTLEMENT_ADJ', 'PPV',
                                     'REVALUATION', 'MANUAL_ADJ', 'RECALC')),
  qty numeric(28,6) NOT NULL,
  amount_acct numeric(19,4) NOT NULL,
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  financial_date date NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CONSTRAINT cost_transaction_source_key UNIQUE (tenant_id, source_key),
  CHECK (qty = 0 OR kind IN ('PHYSICAL', 'FINANCIAL', 'PPV'))
);
CREATE INDEX IF NOT EXISTS cost_transaction_product_date ON cost_transaction (tenant_id, product_id, financial_date);
CREATE INDEX IF NOT EXISTS cost_transaction_report
  ON cost_transaction (tenant_id, legal_entity_id, valuation_ledger_id, financial_date);
CREATE INDEX IF NOT EXISTS cost_transaction_voucher ON cost_transaction (tenant_id, ledger_entry_id)
  WHERE ledger_entry_id IS NOT NULL;

CREATE OR REPLACE FUNCTION cost_transaction_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'cost_transaction is append-only' USING ERRCODE = 'P0001';
END;
$$;
DROP TRIGGER IF EXISTS cost_transaction_no_update ON cost_transaction;
CREATE TRIGGER cost_transaction_no_update BEFORE UPDATE OR DELETE ON cost_transaction
  FOR EACH ROW EXECUTE FUNCTION cost_transaction_append_only();

ALTER TABLE cost_transaction ENABLE ROW LEVEL SECURITY;
ALTER TABLE cost_transaction FORCE ROW LEVEL SECURITY;
CREATE POLICY cost_transaction_tenant_access ON cost_transaction AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY cost_transaction_legal_entity_scope ON cost_transaction AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
