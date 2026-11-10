-- #49: opening vendor items loaded at cutover. A batch is idempotent per (legal entity, batch key); an
-- item is idempotent per (legal entity, reference). Opening items post nothing to the ledger: the GL
-- opening journal already carries the AP control balance.
CREATE TABLE IF NOT EXISTS ap_opening_batch (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  batch_key text NOT NULL CHECK (batch_key <> ''),
  content_hash text NOT NULL,
  item_count integer NOT NULL CHECK (item_count > 0),
  control_totals jsonb NOT NULL,
  imported_by text NOT NULL,
  imported_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, batch_key)
);
ALTER TABLE ap_opening_batch ENABLE ROW LEVEL SECURITY;
ALTER TABLE ap_opening_batch FORCE ROW LEVEL SECURITY;
CREATE POLICY ap_opening_batch_tenant_access ON ap_opening_batch AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY ap_opening_batch_legal_entity_scope ON ap_opening_batch AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE ap_open_item DROP CONSTRAINT IF EXISTS ap_open_item_due_date_source_check;
ALTER TABLE ap_open_item ADD CONSTRAINT ap_open_item_due_date_source_check CHECK (due_date_source IN ('EVENT', 'DEFAULTED', 'OPENING'));
ALTER TABLE ap_open_item ADD COLUMN IF NOT EXISTS opening_batch_id uuid;
ALTER TABLE ap_open_item ADD COLUMN IF NOT EXISTS opening_reference text;
CREATE UNIQUE INDEX IF NOT EXISTS ap_open_item_opening_reference
  ON ap_open_item (tenant_id, legal_entity_id, opening_reference) WHERE opening_reference IS NOT NULL;
CREATE INDEX IF NOT EXISTS ap_open_item_opening_batch ON ap_open_item (tenant_id, opening_batch_id) WHERE opening_batch_id IS NOT NULL;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON ap_opening_batch TO passage_app';
  END IF;
END
$$;
