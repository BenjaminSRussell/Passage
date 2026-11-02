-- Pricing parameters per legal entity (PRD 5.26.13) and maker/checker on price journals
-- (journal_four_eyes, PRD 5.26.17): the journal records who created it and who posted it.
CREATE TABLE IF NOT EXISTS pricing_parameter (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  journal_four_eyes boolean NOT NULL DEFAULT false,
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  updated_by text NOT NULL DEFAULT '' CHECK (length(updated_by) <= 128),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id)
);

ALTER TABLE pricing_parameter ENABLE ROW LEVEL SECURITY;
ALTER TABLE pricing_parameter FORCE ROW LEVEL SECURITY;
CREATE POLICY pricing_parameter_tenant_access ON pricing_parameter AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY pricing_parameter_legal_entity_scope ON pricing_parameter AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE price_journal ADD COLUMN IF NOT EXISTS created_by text NOT NULL DEFAULT '' CHECK (length(created_by) <= 128);
ALTER TABLE price_journal ADD COLUMN IF NOT EXISTS posted_by text NOT NULL DEFAULT '' CHECK (length(posted_by) <= 128);

-- Application role privileges. The store reads and upserts parameters (insert, then update by
-- version). price_journal already has UPDATE. Skipped when the role does not exist.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON pricing_parameter TO passage_app';
  END IF;
END
$$;
