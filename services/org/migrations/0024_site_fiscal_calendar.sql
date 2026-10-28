ALTER TABLE site ADD COLUMN IF NOT EXISTS fiscal_calendar_id uuid;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'site'::regclass AND conname = 'site_fiscal_calendar_fk') THEN
    ALTER TABLE site ADD CONSTRAINT site_fiscal_calendar_fk FOREIGN KEY (tenant_id, fiscal_calendar_id) REFERENCES fiscal_calendar (tenant_id, id);
  END IF;
END $$;
CREATE INDEX IF NOT EXISTS site_fiscal_calendar ON site (tenant_id, fiscal_calendar_id);
