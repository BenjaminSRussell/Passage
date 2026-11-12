-- Opening stock import (#49): a reference (the factory's legacy id of a stock line) opens stock once per legal entity, across
-- batches, not only within one batch. The import reserves the batch's references under its key before it creates the journal;
-- a reference another batch holds is refused (INV-OPB-005), and a resend under the same key finds its own reservations.

CREATE TABLE IF NOT EXISTS inventory_opening_reference (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  reference text NOT NULL CHECK (char_length(reference) BETWEEN 1 AND 255),
  batch_key text NOT NULL CHECK (char_length(batch_key) BETWEEN 1 AND 200),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id, reference)
);
CREATE INDEX IF NOT EXISTS inventory_opening_reference_batch_idx ON inventory_opening_reference (tenant_id, batch_key);
ALTER TABLE inventory_opening_reference ENABLE ROW LEVEL SECURITY;
ALTER TABLE inventory_opening_reference FORCE ROW LEVEL SECURITY;
CREATE POLICY inventory_opening_reference_tenant_access ON inventory_opening_reference AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY inventory_opening_reference_legal_entity_scope ON inventory_opening_reference AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON inventory_opening_reference TO passage_app';
  END IF;
END
$$;
