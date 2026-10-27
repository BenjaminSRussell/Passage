-- Pricing context, wave 26 slice 3: sales groups with rep shares, commission rules and
-- commission transactions (FR-26.18, BR-26.12), and the retroactive flag on rebate
-- accruals (FR-26.23, BR-26.14). Additive only.

CREATE TABLE IF NOT EXISTS sales_group (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key)
);

-- Reps of a sales group in their entered order (seq): the first rep takes the rounding
-- residual (BR-26.12). Shares of one group sum to 100 (checked by the service).
CREATE TABLE IF NOT EXISTS sales_group_rep (
  tenant_id uuid NOT NULL,
  sales_group_id uuid NOT NULL,
  seq integer NOT NULL CHECK (seq >= 1),
  rep_id text NOT NULL CHECK (length(rep_id) BETWEEN 1 AND 64),
  share_pct numeric(9,6) NOT NULL CHECK (share_pct > 0 AND share_pct <= 100),
  PRIMARY KEY (tenant_id, sales_group_id, rep_id),
  UNIQUE (tenant_id, sales_group_id, seq),
  FOREIGN KEY (tenant_id, sales_group_id) REFERENCES sales_group (tenant_id, id)
);

-- One rule per sales group and customer/item commission group; an empty key means all.
CREATE TABLE IF NOT EXISTS commission_rule (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  sales_group_id uuid NOT NULL,
  customer_group text NOT NULL DEFAULT '' CHECK (length(customer_group) <= 64),
  item_group text NOT NULL DEFAULT '' CHECK (length(item_group) <= 64),
  basis text NOT NULL CHECK (basis IN ('REVENUE', 'MARGIN')),
  rate_pct numeric(9,6) NOT NULL CHECK (rate_pct >= 0 AND rate_pct <= 100),
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  UNIQUE (tenant_id, sales_group_id, customer_group, item_group),
  FOREIGN KEY (tenant_id, sales_group_id) REFERENCES sales_group (tenant_id, id)
);

-- One row per rep per invoice line. Credit notes write negative rows that name the line they
-- reverse. line_net and cost_snapshot keep what the invoice feed sent, so a redelivery can be
-- compared and later credits capped at the invoiced net.
CREATE TABLE IF NOT EXISTS commission_transaction (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  invoice_line_id text NOT NULL CHECK (length(invoice_line_id) BETWEEN 1 AND 128),
  reverses_invoice_line_id text CHECK (length(reverses_invoice_line_id) BETWEEN 1 AND 128),
  invoice_date date NOT NULL,
  period_key text NOT NULL,
  customer_id text NOT NULL DEFAULT '',
  customer_group text NOT NULL DEFAULT '',
  item_group text NOT NULL DEFAULT '',
  sales_group_id uuid NOT NULL,
  rep_id text NOT NULL,
  rep_seq integer NOT NULL,
  share_pct numeric(9,6) NOT NULL,
  basis text NOT NULL CHECK (basis IN ('REVENUE', 'MARGIN')),
  line_net numeric(20,6) NOT NULL,
  cost_snapshot numeric(20,6),
  basis_amount numeric(20,6) NOT NULL,
  rate_pct numeric(9,6) NOT NULL,
  amount numeric(20,6) NOT NULL,
  currency char(3) NOT NULL,
  debit_account text NOT NULL,
  credit_account text NOT NULL,
  status text NOT NULL DEFAULT 'POSTED' CHECK (status IN ('POSTED')),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, invoice_line_id, rep_id),
  FOREIGN KEY (tenant_id, sales_group_id) REFERENCES sales_group (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS commission_transaction_rep_ix ON commission_transaction (tenant_id, rep_id, period_key);
CREATE INDEX IF NOT EXISTS commission_transaction_group_ix ON commission_transaction (tenant_id, sales_group_id, period_key);
CREATE INDEX IF NOT EXISTS commission_transaction_reverses_ix ON commission_transaction (tenant_id, reverses_invoice_line_id)
  WHERE reverses_invoice_line_id IS NOT NULL;

-- Retroactive recompute rows (the replayed invoice lines and the per-period delta) are
-- flagged so the accrual inquiry and the ledger can tell them apart (BR-26.7, BR-26.14).
ALTER TABLE rebate_accrual ADD COLUMN IF NOT EXISTS retroactive boolean NOT NULL DEFAULT false;

ALTER TABLE sales_group ENABLE ROW LEVEL SECURITY;
ALTER TABLE sales_group FORCE ROW LEVEL SECURITY;
CREATE POLICY sales_group_tenant_access ON sales_group AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY sales_group_legal_entity_scope ON sales_group AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE sales_group_rep ENABLE ROW LEVEL SECURITY;
ALTER TABLE sales_group_rep FORCE ROW LEVEL SECURITY;
CREATE POLICY sales_group_rep_tenant_access ON sales_group_rep AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE commission_rule ENABLE ROW LEVEL SECURITY;
ALTER TABLE commission_rule FORCE ROW LEVEL SECURITY;
CREATE POLICY commission_rule_tenant_access ON commission_rule AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY commission_rule_legal_entity_scope ON commission_rule AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE commission_transaction ENABLE ROW LEVEL SECURITY;
ALTER TABLE commission_transaction FORCE ROW LEVEL SECURITY;
CREATE POLICY commission_transaction_tenant_access ON commission_transaction AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY commission_transaction_legal_entity_scope ON commission_transaction AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
