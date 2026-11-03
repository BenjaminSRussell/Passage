-- Procedure determination (PRD 5.26.13, parameter reference_procedure): which pricing
-- procedure a document uses, per legal entity and sales channel. An empty sales_channel is
-- the legal entity's default; a channel row overrides it. The name points at the ACTIVE
-- version of that procedure at resolve time, so activating a new version takes effect
-- without touching the assignment.
CREATE TABLE IF NOT EXISTS pricing_procedure_assignment (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  sales_channel text NOT NULL DEFAULT '' CHECK (sales_channel ~ '^([A-Z0-9][A-Z0-9_-]{0,31})?$'),
  procedure_name text NOT NULL CHECK (length(procedure_name) BETWEEN 1 AND 64),
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  updated_by text NOT NULL DEFAULT '' CHECK (length(updated_by) <= 128),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id, sales_channel)
);

ALTER TABLE pricing_procedure_assignment ENABLE ROW LEVEL SECURITY;
ALTER TABLE pricing_procedure_assignment FORCE ROW LEVEL SECURITY;
CREATE POLICY pricing_procedure_assignment_tenant_access ON pricing_procedure_assignment AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY pricing_procedure_assignment_legal_entity_scope ON pricing_procedure_assignment AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Application role privileges. The store inserts, updates by version and deletes an
-- assignment. Skipped when the role does not exist.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON pricing_procedure_assignment TO passage_app';
  END IF;
END
$$;
