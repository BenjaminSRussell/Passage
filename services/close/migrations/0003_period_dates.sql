ALTER TABLE period_status ADD COLUMN IF NOT EXISTS start_date date;
ALTER TABLE period_status ADD COLUMN IF NOT EXISTS end_date date;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'period_status_dates_valid' AND conrelid = 'period_status'::regclass) THEN
    ALTER TABLE period_status ADD CONSTRAINT period_status_dates_valid
      CHECK ((start_date IS NULL AND end_date IS NULL) OR (start_date IS NOT NULL AND end_date IS NOT NULL AND end_date >= start_date));
  END IF;
END
$$;
CREATE INDEX IF NOT EXISTS period_status_resolve ON period_status (tenant_id, legal_entity_id, ledger_id, start_date, end_date) WHERE start_date IS NOT NULL;
