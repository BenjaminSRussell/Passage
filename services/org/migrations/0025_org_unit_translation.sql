CREATE TABLE IF NOT EXISTS org_unit_translation (
  tenant_id uuid NOT NULL,
  owner_type text NOT NULL CHECK (owner_type IN ('legal_entity', 'operating_unit', 'site', 'warehouse')),
  owner_id uuid NOT NULL,
  legal_entity_id uuid,
  language text NOT NULL CHECK (language ~ '^[a-z]{2}(-[A-Z]{2})?$'),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, owner_type, owner_id, language)
);

ALTER TABLE org_unit_translation ENABLE ROW LEVEL SECURITY;
ALTER TABLE org_unit_translation FORCE ROW LEVEL SECURITY;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE schemaname = current_schema() AND tablename = 'org_unit_translation' AND policyname = 'org_unit_translation_tenant_access') THEN
    CREATE POLICY org_unit_translation_tenant_access ON org_unit_translation AS PERMISSIVE
      USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
      WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE schemaname = current_schema() AND tablename = 'org_unit_translation' AND policyname = 'org_unit_translation_legal_entity_scope') THEN
    CREATE POLICY org_unit_translation_legal_entity_scope ON org_unit_translation AS RESTRICTIVE
      USING (current_setting('passage.legal_entity_scopes', true) = '*'
             OR legal_entity_id IS NULL
             OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
  END IF;
END $$;
