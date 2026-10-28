ALTER TABLE site ADD COLUMN IF NOT EXISTS default_warehouse_id uuid;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'site'::regclass AND conname = 'site_default_warehouse_fk') THEN
    ALTER TABLE site ADD CONSTRAINT site_default_warehouse_fk FOREIGN KEY (tenant_id, legal_entity_id, default_warehouse_id) REFERENCES warehouse (tenant_id, legal_entity_id, id) DEFERRABLE INITIALLY DEFERRED;
  END IF;
END $$;
