-- Customer down payments (FR-30.21, special G/L A/F, account 2190) and security
-- deposits (special G/L H, account 2195). A request is a noted item with no posting;
-- receipt posts Dr bank / Cr 2190 or 2195; clearing against an open item posts
-- Dr 2190 or 2195 / Cr 1200 (BR-30.14); a deposit's remainder is released by refund
-- (Dr 2195 / Cr bank). Postings travel on the events until the ledger client exists.

CREATE TABLE IF NOT EXISTS down_payment (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  customer_id uuid NOT NULL,
  sales_order_id uuid,
  kind text NOT NULL CHECK (kind IN ('DOWN_PAYMENT', 'SECURITY_DEPOSIT')),
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  amount numeric(20,6) NOT NULL CHECK (amount > 0),
  cleared_amount numeric(20,6) NOT NULL DEFAULT 0,
  status text NOT NULL DEFAULT 'REQUESTED'
    CHECK (status IN ('REQUESTED', 'RECEIVED', 'CLEARED', 'RELEASED', 'CANCELLED')),
  received_date date,
  idempotency_key text NOT NULL,
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  CHECK (cleared_amount >= 0 AND cleared_amount <= amount),
  CHECK ((status IN ('REQUESTED', 'CANCELLED')) = (received_date IS NULL)),
  CHECK (kind = 'SECURITY_DEPOSIT' OR status <> 'RELEASED')
);
CREATE INDEX IF NOT EXISTS down_payment_customer_idx ON down_payment (tenant_id, customer_id, status);
ALTER TABLE down_payment ENABLE ROW LEVEL SECURITY;
ALTER TABLE down_payment FORCE ROW LEVEL SECURITY;
CREATE POLICY down_payment_tenant_access ON down_payment AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY down_payment_legal_entity_scope ON down_payment AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- One row per clearing, so an open item's history (amount - settlements - clearings)
-- and the as-of balance of a balance confirmation stay reconstructible.
CREATE TABLE IF NOT EXISTS down_payment_clearing (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  down_payment_id uuid NOT NULL,
  open_item_id uuid NOT NULL,
  amount numeric(20,6) NOT NULL CHECK (amount > 0),
  cleared_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, down_payment_id) REFERENCES down_payment (tenant_id, id),
  FOREIGN KEY (tenant_id, open_item_id) REFERENCES open_item (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS down_payment_clearing_item_idx ON down_payment_clearing (tenant_id, open_item_id);
CREATE INDEX IF NOT EXISTS down_payment_clearing_dp_idx ON down_payment_clearing (tenant_id, down_payment_id);
ALTER TABLE down_payment_clearing ENABLE ROW LEVEL SECURITY;
ALTER TABLE down_payment_clearing FORCE ROW LEVEL SECURITY;
CREATE POLICY down_payment_clearing_tenant_access ON down_payment_clearing AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY down_payment_clearing_legal_entity_scope ON down_payment_clearing AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
