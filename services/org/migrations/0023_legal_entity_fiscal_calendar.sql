ALTER TABLE legal_entity ADD COLUMN IF NOT EXISTS fiscal_calendar_id uuid;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'legal_entity_fiscal_calendar_fk') THEN
    ALTER TABLE legal_entity ADD CONSTRAINT legal_entity_fiscal_calendar_fk FOREIGN KEY (tenant_id, fiscal_calendar_id) REFERENCES fiscal_calendar (tenant_id, id);
  END IF;
END $$;
CREATE INDEX IF NOT EXISTS legal_entity_fiscal_calendar ON legal_entity (tenant_id, fiscal_calendar_id);
