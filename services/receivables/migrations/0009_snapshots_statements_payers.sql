-- Wave 30 slice 5: payer identity for matching (BR-30.1 R4, R5, R7) and dated
-- customer balance snapshots (§5.30.16 aging snapshot job, customerbalance.changed.v1).
-- Customer statements (FR-30.13) read existing tables and add none.

-- R4 compares the payer account hash (hex SHA-256 of the normalized IBAN or MICR
-- account; the account itself is never stored). R7 needs the short-pay reason.
ALTER TABLE payment ADD COLUMN IF NOT EXISTS payer_account_hash text
  CHECK (payer_account_hash IS NULL OR payer_account_hash ~ '^[0-9a-f]{64}$');
ALTER TABLE payment ADD COLUMN IF NOT EXISTS short_pay_reason text
  CHECK (short_pay_reason IS NULL OR length(short_pay_reason) BETWEEN 1 AND 120);

-- Payer account hashes bound to a customer, learned when a payment is applied (R4).
CREATE TABLE IF NOT EXISTS payer_account_binding (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  payer_account_hash text NOT NULL CHECK (payer_account_hash ~ '^[0-9a-f]{64}$'),
  customer_id uuid NOT NULL,
  learned_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id, payer_account_hash, customer_id)
);
ALTER TABLE payer_account_binding ENABLE ROW LEVEL SECURITY;
ALTER TABLE payer_account_binding FORCE ROW LEVEL SECURITY;
CREATE POLICY payer_account_binding_tenant_access ON payer_account_binding AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY payer_account_binding_legal_entity_scope ON payer_account_binding AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Normalized payer names applied to a customer, learned on apply (R5).
CREATE TABLE IF NOT EXISTS payer_name_binding (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  customer_id uuid NOT NULL,
  payer_name text NOT NULL CHECK (length(payer_name) BETWEEN 1 AND 200),
  learned_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id, customer_id, payer_name)
);
ALTER TABLE payer_name_binding ENABLE ROW LEVEL SECURITY;
ALTER TABLE payer_name_binding FORCE ROW LEVEL SECURITY;
CREATE POLICY payer_name_binding_tenant_access ON payer_name_binding AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY payer_name_binding_legal_entity_scope ON payer_name_binding AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- One balance per customer, currency and key date (job key = (customer, date)).
CREATE TABLE IF NOT EXISTS customer_balance_snapshot (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  customer_id uuid NOT NULL,
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  as_of date NOT NULL,
  total_open numeric(20,6) NOT NULL CHECK (total_open >= 0),
  overdue numeric(20,6) NOT NULL CHECK (overdue >= 0 AND overdue <= total_open),
  oldest_item_days integer NOT NULL CHECK (oldest_item_days >= 0),
  dunning_level smallint NOT NULL CHECK (dunning_level BETWEEN 0 AND 4),
  risk_score smallint NOT NULL CHECK (risk_score BETWEEN 0 AND 100),
  taken_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id, customer_id, currency, as_of)
);
CREATE INDEX IF NOT EXISTS customer_balance_snapshot_date_idx ON customer_balance_snapshot (tenant_id, legal_entity_id, as_of DESC);
ALTER TABLE customer_balance_snapshot ENABLE ROW LEVEL SECURITY;
ALTER TABLE customer_balance_snapshot FORCE ROW LEVEL SECURITY;
CREATE POLICY customer_balance_snapshot_tenant_access ON customer_balance_snapshot AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY customer_balance_snapshot_legal_entity_scope ON customer_balance_snapshot AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- The statement reads settlements and clearings by item and payment up to its date.
CREATE INDEX IF NOT EXISTS settlement_payment_idx ON settlement (tenant_id, payment_id);
CREATE INDEX IF NOT EXISTS payment_customer_idx ON payment (tenant_id, customer_id, received_date);
