DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'legal_entity'::regclass AND conname = 'legal_entity_fiscal_calendar_fk') THEN
    ALTER TABLE legal_entity ADD CONSTRAINT legal_entity_fiscal_calendar_fk FOREIGN KEY (tenant_id, fiscal_calendar_id) REFERENCES fiscal_calendar (tenant_id, id);
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'site'::regclass AND conname = 'site_fiscal_calendar_fk') THEN
    ALTER TABLE site ADD CONSTRAINT site_fiscal_calendar_fk FOREIGN KEY (tenant_id, fiscal_calendar_id) REFERENCES fiscal_calendar (tenant_id, id);
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'site'::regclass AND conname = 'site_default_warehouse_fk') THEN
    ALTER TABLE site ADD CONSTRAINT site_default_warehouse_fk FOREIGN KEY (tenant_id, legal_entity_id, default_warehouse_id) REFERENCES warehouse (tenant_id, legal_entity_id, id) DEFERRABLE INITIALLY DEFERRED;
  END IF;
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
