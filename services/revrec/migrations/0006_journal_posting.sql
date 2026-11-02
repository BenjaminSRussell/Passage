-- Ledger postings of recognized revenue (Dr 2400 contract liability / Cr 4000
-- revenue). One row per entity, currency and month of recognition; the schedule
-- lines it covers carry its id in revenue_schedule.journal_posting_id. A row is
-- created pending (entry_id null) in the transaction that claims its lines and is
-- completed once the ledger has answered, so a failed post is retried with the
-- same ledger idempotency key.

CREATE TABLE IF NOT EXISTS journal_posting (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  ledger_id uuid NOT NULL,
  currency char(3) NOT NULL,
  posting_date date NOT NULL,
  amount numeric(20,6) NOT NULL CHECK (amount > 0),
  line_count integer NOT NULL CHECK (line_count > 0),
  entry_id text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id)
);
ALTER TABLE journal_posting ENABLE ROW LEVEL SECURITY;
ALTER TABLE journal_posting FORCE ROW LEVEL SECURITY;
CREATE POLICY journal_posting_tenant_access ON journal_posting AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY journal_posting_legal_entity_scope ON journal_posting AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
CREATE INDEX IF NOT EXISTS journal_posting_pending_idx ON journal_posting (tenant_id, legal_entity_id) WHERE entry_id IS NULL;
CREATE INDEX IF NOT EXISTS revenue_schedule_unposted_idx ON revenue_schedule (tenant_id, recognized_on) WHERE status = 'RECOGNIZED' AND journal_posting_id IS NULL;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON journal_posting TO passage_app';
  END IF;
END
$$;
