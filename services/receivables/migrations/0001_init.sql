CREATE TABLE IF NOT EXISTS open_item (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  customer_id uuid NOT NULL,
  source_type text NOT NULL CHECK (source_type IN ('INVOICE', 'CREDIT_NOTE', 'PAYMENT', 'INTEREST', 'FEE', 'ADJUSTMENT', 'REBATE', 'DEDUCTION')),
  document_number text NOT NULL,
  document_date date NOT NULL,
  due_date date NOT NULL,
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  amount numeric(20,6) NOT NULL CHECK (amount > 0),
  open_amount numeric(20,6) NOT NULL CHECK (open_amount >= 0 AND open_amount <= amount),
  cash_discount_date date,
  cash_discount_amount numeric(20,6) NOT NULL DEFAULT 0 CHECK (cash_discount_amount >= 0),
  dunning_level smallint NOT NULL DEFAULT 0 CHECK (dunning_level BETWEEN 0 AND 4),
  last_dunned_at date,
  dunning_block boolean NOT NULL DEFAULT false,
  dispute_id uuid,
  promise_id uuid,
  status text NOT NULL CHECK (status IN ('OPEN', 'PARTIALLY_SETTLED', 'SETTLED', 'REVERSED')),
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, document_number),
  CHECK ((open_amount = 0) = (status = 'SETTLED'))
);
CREATE INDEX IF NOT EXISTS open_item_customer_status_due ON open_item (tenant_id, customer_id, status, due_date);
ALTER TABLE open_item ENABLE ROW LEVEL SECURITY;
ALTER TABLE open_item FORCE ROW LEVEL SECURITY;
CREATE POLICY open_item_tenant_access ON open_item AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY open_item_legal_entity_scope ON open_item AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS payment (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  customer_id uuid,
  payment_mode text NOT NULL CHECK (payment_mode IN ('CHECK', 'ACH', 'WIRE', 'CARD', 'CASH')),
  source text NOT NULL CHECK (source IN ('MANUAL', 'LOCKBOX', 'CAMT053', 'PORTAL', 'REMITTANCE')),
  source_ref text,
  amount numeric(20,6) NOT NULL CHECK (amount > 0),
  unapplied_amount numeric(20,6) NOT NULL CHECK (unapplied_amount >= 0 AND unapplied_amount <= amount),
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  received_date date NOT NULL,
  value_date date NOT NULL,
  payer_name text,
  remittance jsonb NOT NULL DEFAULT '[]'::jsonb,
  status text NOT NULL CHECK (status IN ('RECEIVED', 'SUGGESTED', 'APPLIED', 'ON_ACCOUNT', 'REVERSED')),
  journal_posting_id uuid,
  idempotency_key text,
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  UNIQUE (tenant_id, source, source_ref)
);
CREATE INDEX IF NOT EXISTS payment_status_received ON payment (tenant_id, status, received_date);
ALTER TABLE payment ENABLE ROW LEVEL SECURITY;
ALTER TABLE payment FORCE ROW LEVEL SECURITY;
CREATE POLICY payment_tenant_access ON payment AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY payment_legal_entity_scope ON payment AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Settlements are partitioned by month in a later slice (§5.30.19).
CREATE TABLE IF NOT EXISTS settlement (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  payment_id uuid NOT NULL,
  open_item_id uuid NOT NULL,
  settled_amount numeric(20,6) NOT NULL CHECK (settled_amount >= 0),
  cash_discount_taken numeric(20,6) NOT NULL DEFAULT 0 CHECK (cash_discount_taken >= 0),
  write_off_amount numeric(20,6) NOT NULL DEFAULT 0 CHECK (write_off_amount >= 0),
  fx_gain_loss numeric(20,6) NOT NULL DEFAULT 0,
  settled_at timestamptz NOT NULL DEFAULT now(),
  reversal_of_id uuid,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, payment_id) REFERENCES payment (tenant_id, id),
  FOREIGN KEY (tenant_id, open_item_id) REFERENCES open_item (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS settlement_open_item ON settlement (tenant_id, open_item_id);
ALTER TABLE settlement ENABLE ROW LEVEL SECURITY;
ALTER TABLE settlement FORCE ROW LEVEL SECURITY;
CREATE POLICY settlement_tenant_access ON settlement AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY settlement_legal_entity_scope ON settlement AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
