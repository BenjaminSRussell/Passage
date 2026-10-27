-- Billing slice 1: invoices (proposal and posted), their lines, the gap-free
-- number sequence and the transactional outbox. Every table carries tenant_id
-- first and enables row-level security (dbx.RLSPolicy); tables with
-- legal_entity_id also carry the restrictive legal-entity scope policy.

CREATE TABLE IF NOT EXISTS invoice (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  customer_id uuid NOT NULL,
  invoice_account_id uuid NOT NULL,
  invoice_type text NOT NULL CHECK (invoice_type IN ('SHIPMENT', 'CONSOLIDATED', 'FREE_TEXT', 'SUBSCRIPTION', 'MILESTONE', 'CREDIT_NOTE')),
  status text NOT NULL CHECK (status IN ('PROPOSAL', 'POSTED', 'PARTIALLY_SETTLED', 'SETTLED', 'REVERSED')),
  invoice_number text,
  fiscal_year integer,
  invoice_date date NOT NULL,
  posting_date date,
  due_date date NOT NULL,
  payment_term_days integer NOT NULL CHECK (payment_term_days BETWEEN 0 AND 365),
  currency char(3) NOT NULL,
  net_amount numeric(20,6) NOT NULL,
  charge_amount numeric(20,6) NOT NULL,
  tax_amount numeric(20,6) NOT NULL,
  gross_amount numeric(20,6) NOT NULL,
  journal_posting_id uuid,
  idempotency_key text NOT NULL,
  request_hash text NOT NULL,
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  UNIQUE (tenant_id, legal_entity_id, invoice_number),
  CHECK (gross_amount = net_amount + charge_amount + tax_amount),
  CHECK (status = 'PROPOSAL' OR (invoice_number IS NOT NULL AND fiscal_year IS NOT NULL AND posting_date IS NOT NULL))
);
ALTER TABLE invoice ENABLE ROW LEVEL SECURITY;
ALTER TABLE invoice FORCE ROW LEVEL SECURITY;
CREATE POLICY invoice_tenant_access ON invoice AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY invoice_legal_entity_scope ON invoice AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
-- Invoices list (newest first) and the due-date index of §5.29.4.
CREATE INDEX IF NOT EXISTS invoice_created_idx ON invoice (tenant_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS invoice_account_status_due_idx ON invoice (tenant_id, invoice_account_id, status, due_date);

CREATE TABLE IF NOT EXISTS invoice_line (
  tenant_id uuid NOT NULL,
  invoice_id uuid NOT NULL,
  line_no integer NOT NULL CHECK (line_no > 0),
  description text NOT NULL,
  qty numeric(20,6) NOT NULL CHECK (qty > 0),
  unit_price numeric(20,6) NOT NULL CHECK (unit_price >= 0),
  disc_pct numeric(9,6) NOT NULL CHECK (disc_pct BETWEEN 0 AND 100),
  line_net numeric(20,6) NOT NULL,
  tax_amount numeric(20,6) NOT NULL,
  revenue_account text NOT NULL,
  tax_code text NOT NULL,
  PRIMARY KEY (tenant_id, invoice_id, line_no),
  FOREIGN KEY (tenant_id, invoice_id) REFERENCES invoice (tenant_id, id)
);
ALTER TABLE invoice_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE invoice_line FORCE ROW LEVEL SECURITY;
CREATE POLICY invoice_line_tenant_access ON invoice_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Gap-free numbers: one row per (entity, sequence, fiscal year), incremented in
-- the posting transaction so a rolled-back post consumes no number (BR-29.9).
CREATE TABLE IF NOT EXISTS number_sequence_counter (
  tenant_id uuid NOT NULL,
  sequence_id text NOT NULL,
  legal_entity_id uuid NOT NULL,
  fiscal_year integer NOT NULL,
  last_value bigint NOT NULL DEFAULT 0,
  PRIMARY KEY (tenant_id, sequence_id, legal_entity_id, fiscal_year)
) WITH (fillfactor = 50);
ALTER TABLE number_sequence_counter ENABLE ROW LEVEL SECURITY;
ALTER TABLE number_sequence_counter FORCE ROW LEVEL SECURITY;
CREATE POLICY number_sequence_counter_tenant_access ON number_sequence_counter AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY number_sequence_counter_legal_entity_scope ON number_sequence_counter AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Transactional outbox relayed by Debezium (platform dbx.OutboxDDL); excluded from RLS.
CREATE TABLE IF NOT EXISTS outbox (
  id text primary key,
  tenant_id uuid not null,
  aggregate_type text not null,
  aggregate_id text not null,
  type text not null,
  payload jsonb not null,
  created_at timestamptz not null default now()
);
ALTER TABLE outbox REPLICA IDENTITY FULL;
