-- Ledger journals receivables owes the general ledger (30-2): cash receipts, applications, cash discount and write-off,
-- dunning fees, interest, down payments and deduction write-offs. A row is stored in the transaction of the business
-- change that causes it, so AR never changes without its journal being owed; a worker posts it through the ledger
-- under the idempotency key gl-journal:<legal entity>:<source key>.
CREATE TABLE IF NOT EXISTS gl_journal (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  source_type text NOT NULL,
  source_id uuid NOT NULL,
  source_key text NOT NULL,
  document_id text NOT NULL,
  posting_date date NOT NULL,
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  lines jsonb NOT NULL,
  status text NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'POSTED')),
  journal_posting_id text,
  attempts int NOT NULL DEFAULT 0,
  next_attempt_at timestamptz,
  parked boolean NOT NULL DEFAULT false,
  last_error text,
  created_at timestamptz NOT NULL DEFAULT now(),
  posted_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, source_key),
  CHECK ((status = 'POSTED') = (journal_posting_id IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS gl_journal_pending ON gl_journal (tenant_id, created_at) WHERE status = 'PENDING';
ALTER TABLE gl_journal ENABLE ROW LEVEL SECURITY;
ALTER TABLE gl_journal FORCE ROW LEVEL SECURITY;
CREATE POLICY gl_journal_tenant_access ON gl_journal AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY gl_journal_legal_entity_scope ON gl_journal AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON gl_journal TO passage_app';
  END IF;
END
$$;
