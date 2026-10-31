-- Wave 16 slice 12: journal names (PRD 3.16.13). A journal name is the setup row a
-- keyed journal is created under: it fixes the journal type, may require the
-- approval workflow before posting (BR-16.11) and may carry a default offset
-- account for MOVEMENT journals.
CREATE TABLE IF NOT EXISTS inventory_journal_name (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  id uuid NOT NULL,
  idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 255),
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 200),
  journal_type text NOT NULL CHECK (journal_type IN ('MOVEMENT', 'ADJUSTMENT', 'OPENING_BALANCE', 'OWNERSHIP_CHANGE', 'TRANSFER')),
  requires_approval boolean NOT NULL DEFAULT false,
  offset_account_id text CHECK (offset_account_id IS NULL OR (journal_type = 'MOVEMENT' AND offset_account_id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$')),
  active boolean NOT NULL DEFAULT true,
  version integer NOT NULL DEFAULT 1,
  created_by text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CONSTRAINT inventory_journal_name_idempotency_key_uniq UNIQUE (tenant_id, idempotency_key),
  CONSTRAINT inventory_journal_name_code_uniq UNIQUE (tenant_id, legal_entity_id, code),
  -- Target of the journal's foreign key, so a journal can only reference a name of its own legal entity.
  CONSTRAINT inventory_journal_name_entity_uniq UNIQUE (tenant_id, legal_entity_id, id)
);
ALTER TABLE inventory_journal_name ENABLE ROW LEVEL SECURITY;
ALTER TABLE inventory_journal_name FORCE ROW LEVEL SECURITY;
CREATE POLICY inventory_journal_name_tenant_access ON inventory_journal_name AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY inventory_journal_name_legal_entity_scope ON inventory_journal_name AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE inventory_journal ADD COLUMN IF NOT EXISTS journal_name_id uuid;
ALTER TABLE inventory_journal DROP CONSTRAINT IF EXISTS inventory_journal_name_fk;
ALTER TABLE inventory_journal ADD CONSTRAINT inventory_journal_name_fk
  FOREIGN KEY (tenant_id, legal_entity_id, journal_name_id) REFERENCES inventory_journal_name (tenant_id, legal_entity_id, id);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON inventory_journal_name TO passage_app';
  END IF;
END
$$;
