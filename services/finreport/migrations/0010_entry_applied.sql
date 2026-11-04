-- PSG-1508 (wave 15, slice 8): the ledger consumer (FR-15.1).
-- One row per journal entry the read model has applied, keyed by the ledger's
-- gap-free posting sequence, so a replayed event is recognised, a reversal can
-- find the entry it reverses (closing marks whether that entry was a year-end
-- closing), and the lag panel can show how many entries each ledger delivered.
CREATE TABLE IF NOT EXISTS reporting_entry_applied (
  tenant_id uuid NOT NULL,
  ledger_id uuid NOT NULL,
  posting_sequence bigint NOT NULL CHECK (posting_sequence >= 1),
  entry_id uuid NOT NULL,
  line_count integer NOT NULL CHECK (line_count >= 1),
  closing boolean NOT NULL DEFAULT false,
  applied_at timestamptz NOT NULL DEFAULT now(),
  legal_entity_id uuid NOT NULL,
  PRIMARY KEY (tenant_id, ledger_id, posting_sequence),
  UNIQUE (tenant_id, entry_id)
);
ALTER TABLE reporting_entry_applied ENABLE ROW LEVEL SECURITY;
ALTER TABLE reporting_entry_applied FORCE ROW LEVEL SECURITY;
CREATE POLICY reporting_entry_applied_tenant_access ON reporting_entry_applied AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY reporting_entry_applied_legal_entity_scope ON reporting_entry_applied AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Entries that arrived ahead of the ledger's high-water mark. The ledger keys
-- journal.posted by entry, so one ledger's entries reach the consumer through
-- several partitions and out of order; an early entry waits here (lines
-- included) and is applied, then deleted, as soon as the mark reaches it.
CREATE TABLE IF NOT EXISTS reporting_entry_pending (
  tenant_id uuid NOT NULL,
  ledger_id uuid NOT NULL,
  posting_sequence bigint NOT NULL CHECK (posting_sequence >= 1),
  entry_id uuid NOT NULL,
  entry jsonb NOT NULL,
  received_at timestamptz NOT NULL DEFAULT now(),
  legal_entity_id uuid NOT NULL,
  PRIMARY KEY (tenant_id, ledger_id, posting_sequence),
  UNIQUE (tenant_id, entry_id)
);
ALTER TABLE reporting_entry_pending ENABLE ROW LEVEL SECURITY;
ALTER TABLE reporting_entry_pending FORCE ROW LEVEL SECURITY;
CREATE POLICY reporting_entry_pending_tenant_access ON reporting_entry_pending AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY reporting_entry_pending_legal_entity_scope ON reporting_entry_pending AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- The consumer now writes the read model: balances are upserted (UPDATE) and the
-- high-water mark is row-locked and advanced (UPDATE). Applied entries are
-- append-only; a pending entry is deleted once applied.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON reporting_balance, reporting_hwm TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON reporting_entry_applied TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON reporting_entry_pending TO passage_app';
  END IF;
END
$$;
