-- Pricing context, wave 26 slice 2: multiline and surcharge condition types,
-- atomic price journals with reversal (FR-26.9) and customer rebate agreements
-- with period-cumulative accrual (FR-26.11, BR-26.8).

-- Widen the condition type set. The 0001 check is inline, so Postgres named it.
ALTER TABLE condition_record DROP CONSTRAINT IF EXISTS condition_record_condition_type_check;
ALTER TABLE condition_record ADD CONSTRAINT condition_record_condition_type_check
  CHECK (condition_type IN ('PRICE', 'LINE_DISC', 'MULTILINE_DISC', 'TOTAL_DISC', 'SURCHARGE'));
-- The journal that last changed the record (PRD 5.26.4: every ACTIVE record has a journal_id once journals post).
ALTER TABLE condition_record ADD COLUMN IF NOT EXISTS journal_id uuid;

CREATE TABLE IF NOT EXISTS price_journal (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
  status text NOT NULL CHECK (status IN ('OPEN', 'VALIDATED', 'POSTED', 'REVERSED')),
  smart_round boolean NOT NULL DEFAULT false,
  line_count integer NOT NULL CHECK (line_count >= 0),
  error_count integer NOT NULL DEFAULT 0 CHECK (error_count >= 0),
  reverses_journal_id uuid,
  reversed_by_journal_id uuid,
  posted_at timestamptz,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key)
);

CREATE TABLE IF NOT EXISTS price_journal_line (
  tenant_id uuid NOT NULL,
  journal_id uuid NOT NULL,
  line_no integer NOT NULL CHECK (line_no >= 1),
  op text NOT NULL CHECK (op IN ('CREATE', 'UPDATE', 'DELETE', 'RESTORE')),
  record_id uuid NOT NULL,
  expected_version integer NOT NULL DEFAULT 0,
  adjust_pct numeric(9,4) NOT NULL DEFAULT 0,
  record jsonb NOT NULL,
  before jsonb,
  after jsonb,
  after_version integer NOT NULL DEFAULT 0,
  errors jsonb,
  PRIMARY KEY (tenant_id, journal_id, line_no),
  FOREIGN KEY (tenant_id, journal_id) REFERENCES price_journal (tenant_id, id)
);

ALTER TABLE price_journal ENABLE ROW LEVEL SECURITY;
ALTER TABLE price_journal FORCE ROW LEVEL SECURITY;
CREATE POLICY price_journal_tenant_access ON price_journal AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY price_journal_legal_entity_scope ON price_journal AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE price_journal_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE price_journal_line FORCE ROW LEVEL SECURITY;
CREATE POLICY price_journal_line_tenant_access ON price_journal_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS rebate_agreement (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
  customer_id uuid,
  customer_group_id uuid,
  basis text NOT NULL CHECK (basis IN ('NET_AMOUNT', 'QUANTITY')),
  period text NOT NULL CHECK (period IN ('MONTH', 'QUARTER', 'YEAR')),
  tiers jsonb NOT NULL,
  currency char(3) NOT NULL,
  valid_from date NOT NULL,
  valid_to date NOT NULL,
  accrual_account text NOT NULL DEFAULT '2150',
  expense_account text NOT NULL DEFAULT '4150',
  status text NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE')),
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CHECK ((customer_id IS NULL) <> (customer_group_id IS NULL)),
  CHECK (valid_from <= valid_to),
  UNIQUE (tenant_id, idempotency_key)
);

-- Cumulative state of one agreement in one period. Accruals lock this row, so
-- concurrent invoices for the same period serialize.
CREATE TABLE IF NOT EXISTS rebate_period (
  tenant_id uuid NOT NULL,
  agreement_id uuid NOT NULL,
  period_key text NOT NULL,
  basis_value numeric(20,6) NOT NULL DEFAULT 0,
  basis_qty numeric(20,6) NOT NULL DEFAULT 0,
  accrued_total numeric(20,6) NOT NULL DEFAULT 0,
  PRIMARY KEY (tenant_id, agreement_id, period_key),
  FOREIGN KEY (tenant_id, agreement_id) REFERENCES rebate_agreement (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS rebate_accrual (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  agreement_id uuid NOT NULL,
  period_key text NOT NULL,
  invoice_line_id text NOT NULL CHECK (length(invoice_line_id) BETWEEN 1 AND 128),
  invoice_date date NOT NULL,
  basis_value numeric(20,6) NOT NULL,
  basis_qty numeric(20,6) NOT NULL,
  accrued_amount numeric(20,6) NOT NULL,
  currency char(3) NOT NULL,
  status text NOT NULL DEFAULT 'ACCRUED' CHECK (status IN ('ACCRUED', 'ADJUSTED', 'SETTLED', 'REVERSED')),
  journal_posting_id uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, agreement_id, invoice_line_id),
  FOREIGN KEY (tenant_id, agreement_id, period_key) REFERENCES rebate_period (tenant_id, agreement_id, period_key)
);
CREATE INDEX IF NOT EXISTS rebate_accrual_period_ix ON rebate_accrual (tenant_id, agreement_id, period_key);

ALTER TABLE rebate_agreement ENABLE ROW LEVEL SECURITY;
ALTER TABLE rebate_agreement FORCE ROW LEVEL SECURITY;
CREATE POLICY rebate_agreement_tenant_access ON rebate_agreement AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY rebate_agreement_legal_entity_scope ON rebate_agreement AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE rebate_period ENABLE ROW LEVEL SECURITY;
ALTER TABLE rebate_period FORCE ROW LEVEL SECURITY;
CREATE POLICY rebate_period_tenant_access ON rebate_period AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE rebate_accrual ENABLE ROW LEVEL SECURITY;
ALTER TABLE rebate_accrual FORCE ROW LEVEL SECURITY;
CREATE POLICY rebate_accrual_tenant_access ON rebate_accrual AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY rebate_accrual_legal_entity_scope ON rebate_accrual AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
