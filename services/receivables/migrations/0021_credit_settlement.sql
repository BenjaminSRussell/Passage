-- Credit items settled against debits (30-3): a credit note, an adjustment or an unapplied payment item is offset against
-- the customer's invoice items. Nothing moves in the ledger (everything stays in 1200); both items reduce by the amount.
-- One row per (credit item, debit item) pair; amount is a positive magnitude. The as-of statement and balance
-- confirmation add settlements made after their date back, on both sides.
CREATE TABLE IF NOT EXISTS credit_settlement (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  batch_id uuid NOT NULL,
  customer_id uuid NOT NULL,
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  credit_item_id uuid NOT NULL,
  debit_item_id uuid NOT NULL,
  amount numeric(20,6) NOT NULL CHECK (amount > 0),
  settled_at timestamptz NOT NULL DEFAULT now(),
  settled_by text NOT NULL DEFAULT '',
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, credit_item_id) REFERENCES open_item (tenant_id, id),
  FOREIGN KEY (tenant_id, debit_item_id) REFERENCES open_item (tenant_id, id),
  CHECK (credit_item_id <> debit_item_id)
);
CREATE INDEX IF NOT EXISTS credit_settlement_credit ON credit_settlement (tenant_id, credit_item_id);
CREATE INDEX IF NOT EXISTS credit_settlement_debit ON credit_settlement (tenant_id, debit_item_id);
ALTER TABLE credit_settlement ENABLE ROW LEVEL SECURITY;
ALTER TABLE credit_settlement FORCE ROW LEVEL SECURITY;
CREATE POLICY credit_settlement_tenant_access ON credit_settlement AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY credit_settlement_legal_entity_scope ON credit_settlement AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON credit_settlement TO passage_app';
  END IF;
END
$$;
