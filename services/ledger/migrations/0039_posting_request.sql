-- Posting-request registry (D30, B-47b): one row per posting key the ledger has seen, with its status and the journal
-- entry it produced. A posted row is written in the posting transaction itself, so a rolled-back post leaves no row and
-- a committed post always has one; a rejected row is written after the rollback. status 'received' is for a caller that
-- registers a key before it posts (the inventory valuation reservation); the ledger writes only posted and rejected.
-- Expand-only: a new table, no change to existing ones.
CREATE TABLE IF NOT EXISTS posting_request (
  tenant_id       uuid        NOT NULL,
  legal_entity_id uuid        NOT NULL,
  ledger_id       uuid        NOT NULL,
  idempotency_key text        NOT NULL CHECK (char_length(idempotency_key) >= 1),
  status          text        NOT NULL CHECK (status IN ('received', 'posted', 'rejected')),
  journal_entry_id uuid,
  document_number text,
  violation_code  text,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id, idempotency_key),
  CHECK ((status = 'posted') = (journal_entry_id IS NOT NULL)),
  FOREIGN KEY (tenant_id, ledger_id) REFERENCES ledger (tenant_id, id),
  FOREIGN KEY (tenant_id, journal_entry_id) REFERENCES journal_entry (tenant_id, id)
);

ALTER TABLE posting_request ENABLE ROW LEVEL SECURITY;
ALTER TABLE posting_request FORCE ROW LEVEL SECURITY;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE schemaname = current_schema() AND tablename = 'posting_request' AND policyname = 'posting_request_tenant_access') THEN
    CREATE POLICY posting_request_tenant_access ON posting_request AS PERMISSIVE
      USING (tenant_id = current_setting('depot.tenant_id', true)::uuid)
      WITH CHECK (tenant_id = current_setting('depot.tenant_id', true)::uuid);
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE schemaname = current_schema() AND tablename = 'posting_request' AND policyname = 'posting_request_legal_entity_scope') THEN
    CREATE POLICY posting_request_legal_entity_scope ON posting_request AS RESTRICTIVE
      USING (current_setting('depot.legal_entity_scopes', true) = '*'
             OR legal_entity_id::text = ANY(string_to_array(current_setting('depot.legal_entity_scopes', true), ',')));
  END IF;
END
$$;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO depot_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON posting_request TO depot_app';
  END IF;
END
$$;
