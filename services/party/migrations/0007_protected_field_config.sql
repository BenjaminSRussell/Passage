CREATE TABLE IF NOT EXISTS protected_field_config (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  fields text[] NOT NULL CHECK (fields <@ ARRAY['credit_limit']::text[]),
  updated_by text NOT NULL CHECK (updated_by <> ''),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id)
);

ALTER TABLE protected_field_config ENABLE ROW LEVEL SECURITY;
ALTER TABLE protected_field_config FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS protected_field_config_tenant_access ON protected_field_config;
CREATE POLICY protected_field_config_tenant_access ON protected_field_config AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
DROP POLICY IF EXISTS protected_field_config_legal_entity_scope ON protected_field_config;
CREATE POLICY protected_field_config_legal_entity_scope ON protected_field_config AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON protected_field_config TO passage_app';
  END IF;
END
$$;
