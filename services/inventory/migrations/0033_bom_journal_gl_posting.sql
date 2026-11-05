-- Wave 16 AC3 (#477): the BOM journal type and the posting of journal GL entries to the
-- general ledger through the ledger service.
--
-- inventory_journal and inventory_journal_name are live tables, so the widened type checks
-- are added NOT VALID (an instant ACCESS EXCLUSIVE lock, no scan) under a 5 s lock_timeout;
-- 0034 validates them. The new set is a superset of the old one, so existing rows pass.
SET LOCAL lock_timeout = '5s';

ALTER TABLE inventory_journal DROP CONSTRAINT IF EXISTS inventory_journal_journal_type_check;
ALTER TABLE inventory_journal ADD CONSTRAINT inventory_journal_journal_type_check
  CHECK (journal_type IN ('MOVEMENT', 'ADJUSTMENT', 'OPENING_BALANCE', 'COUNT', 'OWNERSHIP_CHANGE', 'TRANSFER', 'BOM')) NOT VALID;
ALTER TABLE inventory_journal_name DROP CONSTRAINT IF EXISTS inventory_journal_name_journal_type_check;
ALTER TABLE inventory_journal_name ADD CONSTRAINT inventory_journal_name_journal_type_check
  CHECK (journal_type IN ('MOVEMENT', 'ADJUSTMENT', 'OPENING_BALANCE', 'OWNERSHIP_CHANGE', 'TRANSFER', 'BOM')) NOT VALID;

-- The general ledger a legal entity's journal entries are booked in. The first GL sweep of the
-- legal entity pins it, after the ledger confirmed the legal entity owns it; a sweep naming
-- another ledger is refused, so one legal entity's entries never split across ledgers.
CREATE TABLE IF NOT EXISTS inventory_gl_ledger (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  gl_ledger_id uuid NOT NULL,
  pinned_by text NOT NULL DEFAULT '',
  pinned_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id)
);

-- One row per POSTED journal with a non-empty GL entry, written before the ledger is called.
-- The entry sent is always built from the journal's stored gl_entry, under idempotency_key, so a
-- retry (after a timeout or a lost answer) resends the same entry and the ledger returns the entry
-- it already holds. A failure counts an attempt and delays the next one with exponential backoff
-- (next_attempt_at, capped at one hour); a refusal the ledger will repeat, an entry that cannot
-- balance, or the fifth failure PARKS the row with its reason until it is requeued.
CREATE TABLE IF NOT EXISTS inventory_gl_posting (
  tenant_id uuid NOT NULL,
  journal_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  gl_ledger_id uuid NOT NULL,
  idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 255),
  status text NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'POSTED', 'PARKED')),
  journal_entry_id text,
  attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
  last_error text NOT NULL DEFAULT '',
  parked_reason text,
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  created_at timestamptz NOT NULL DEFAULT now(),
  posted_at timestamptz,
  PRIMARY KEY (tenant_id, journal_id),
  FOREIGN KEY (tenant_id, journal_id) REFERENCES inventory_journal (tenant_id, id),
  CHECK ((status = 'POSTED') = (journal_entry_id IS NOT NULL)),
  CHECK ((status = 'PARKED') = (parked_reason IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS inventory_gl_posting_due_idx
  ON inventory_gl_posting (tenant_id, legal_entity_id, status, next_attempt_at);

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['inventory_gl_ledger', 'inventory_gl_posting'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('DROP POLICY IF EXISTS %I ON %I', t || '_tenant_access', t);
    EXECUTE format($p$CREATE POLICY %I ON %I AS PERMISSIVE
      USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
      WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid)$p$, t || '_tenant_access', t);
    EXECUTE format('DROP POLICY IF EXISTS %I ON %I', t || '_legal_entity_scope', t);
    EXECUTE format($p$CREATE POLICY %I ON %I AS RESTRICTIVE
      USING (current_setting('passage.legal_entity_scopes', true) = '*'
             OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')))$p$,
      t || '_legal_entity_scope', t);
  END LOOP;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON inventory_gl_ledger TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON inventory_gl_posting TO passage_app';
  END IF;
END
$$;
