-- Process manufacturing, wave 24 slice 13: batch genealogy of a stored merge
-- (FR-24.16, AC-24.12). One row per source batch of a merge, pointing at the
-- target batch it went into, with the quantity and cost per unit it
-- contributed. Rows are only inserted and read.

CREATE TABLE IF NOT EXISTS batch_genealogy (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  child_batch_id uuid NOT NULL,
  parent_batch_id uuid NOT NULL,
  relation text NOT NULL CHECK (relation IN ('merge')),
  qty numeric(18,6) NOT NULL CHECK (qty > 0),
  unit_cost numeric(18,6) NOT NULL CHECK (unit_cost >= 0),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, child_batch_id, parent_batch_id),
  CHECK (child_batch_id <> parent_batch_id),
  FOREIGN KEY (tenant_id, child_batch_id) REFERENCES batch_record (tenant_id, id),
  FOREIGN KEY (tenant_id, parent_batch_id) REFERENCES batch_record (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS batch_genealogy_parent_idx ON batch_genealogy (tenant_id, parent_batch_id);

ALTER TABLE batch_genealogy ENABLE ROW LEVEL SECURITY;
ALTER TABLE batch_genealogy FORCE ROW LEVEL SECURITY;
CREATE POLICY batch_genealogy_tenant_access ON batch_genealogy AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY batch_genealogy_legal_entity_scope ON batch_genealogy AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT ON batch_genealogy TO passage_app';
  END IF;
END
$$;
