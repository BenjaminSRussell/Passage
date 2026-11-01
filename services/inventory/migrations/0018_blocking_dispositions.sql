-- Wave 16 slice 17: blocking dispositions (FR-16.8). Additive: three columns on
-- inventory_blocking. expected_release_date is when the hold is expected to end
-- (information only); scrap_journal_id points at the posted ADJUSTMENT journal that wrote a
-- scrapped blocking off; escalated_at records the QUARANTINE to BLOCKED transition. No new
-- table, so no new grant (inventory_blocking is already granted SELECT, INSERT and UPDATE
-- to passage_app).
ALTER TABLE inventory_blocking ADD COLUMN IF NOT EXISTS expected_release_date date;
ALTER TABLE inventory_blocking ADD COLUMN IF NOT EXISTS scrap_journal_id uuid;
ALTER TABLE inventory_blocking ADD COLUMN IF NOT EXISTS escalated_at timestamptz;
ALTER TABLE inventory_blocking ADD COLUMN IF NOT EXISTS escalated_by text;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'inventory_blocking_scrap_journal_fk' AND conrelid = 'inventory_blocking'::regclass) THEN
    ALTER TABLE inventory_blocking ADD CONSTRAINT inventory_blocking_scrap_journal_fk
      FOREIGN KEY (tenant_id, scrap_journal_id) REFERENCES inventory_journal (tenant_id, id);
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'inventory_blocking_scrap_journal_check' AND conrelid = 'inventory_blocking'::regclass) THEN
    ALTER TABLE inventory_blocking ADD CONSTRAINT inventory_blocking_scrap_journal_check
      CHECK ((disposition = 'SCRAP') = (scrap_journal_id IS NOT NULL));
  END IF;
END $$;
