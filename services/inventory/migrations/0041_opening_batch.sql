-- Opening stock import (#49, H-S9-1): one row per opening batch the migration factory sent. The batch is an
-- OPENING_BALANCE journal created, approved and posted by the import; the key makes a resend a replay.

CREATE TABLE IF NOT EXISTS inventory_opening_batch (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  batch_key text NOT NULL CHECK (char_length(batch_key) BETWEEN 1 AND 200),
  content_hash text NOT NULL,
  journal_id uuid NOT NULL,
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  item_count integer NOT NULL CHECK (item_count BETWEEN 1 AND 1000),
  qty_total numeric(28,6) NOT NULL,
  value_total numeric(38,12) NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, batch_key),
  FOREIGN KEY (tenant_id, journal_id) REFERENCES inventory_journal (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS inventory_opening_batch_le_idx ON inventory_opening_batch (tenant_id, legal_entity_id, created_at, batch_key);
ALTER TABLE inventory_opening_batch ENABLE ROW LEVEL SECURITY;
ALTER TABLE inventory_opening_batch FORCE ROW LEVEL SECURITY;
CREATE POLICY inventory_opening_batch_tenant_access ON inventory_opening_batch AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY inventory_opening_batch_legal_entity_scope ON inventory_opening_batch AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT ON inventory_opening_batch TO passage_app';
  END IF;
END
$$;
