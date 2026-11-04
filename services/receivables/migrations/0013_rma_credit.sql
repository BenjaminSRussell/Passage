-- One RMA credit note (AC-30.5). credit_open_item_id is reserved when the credit is stored PENDING; the CREDIT_NOTE
-- open item with that id is created only in the transaction that marks the credit POSTED with its journal.
CREATE TABLE IF NOT EXISTS rma_credit (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  rma_id uuid NOT NULL,
  rma_number text NOT NULL,
  invoice_open_item_id uuid NOT NULL,
  credit_open_item_id uuid NOT NULL,
  customer_id uuid NOT NULL,
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  posting_date date NOT NULL,
  gross numeric(20,6) NOT NULL CHECK (gross > 0),
  restocking numeric(20,6) NOT NULL CHECK (restocking >= 0),
  net numeric(20,6) NOT NULL CHECK (net > 0),
  ledger_id uuid NOT NULL,
  request_hash text NOT NULL,
  status text NOT NULL CHECK (status IN ('PENDING', 'POSTED')),
  journal_posting_id text,
  gl_attempts int NOT NULL DEFAULT 0,
  gl_next_attempt_at timestamptz,
  gl_parked boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, rma_id),
  CHECK (gross = restocking + net),
  CHECK ((status = 'POSTED') = (journal_posting_id IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS rma_credit_invoice ON rma_credit (tenant_id, invoice_open_item_id);
-- The sweeper reads the PENDING credits whose backoff ran out; POSTED ones are the bulk and stay out of the index.
CREATE INDEX IF NOT EXISTS rma_credit_pending ON rma_credit (tenant_id, created_at) WHERE status = 'PENDING';
ALTER TABLE rma_credit ENABLE ROW LEVEL SECURITY;
ALTER TABLE rma_credit FORCE ROW LEVEL SECURITY;
CREATE POLICY rma_credit_tenant_access ON rma_credit AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY rma_credit_legal_entity_scope ON rma_credit AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON rma_credit TO passage_app';
  END IF;
END
$$;
