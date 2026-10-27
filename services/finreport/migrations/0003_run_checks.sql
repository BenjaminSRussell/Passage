-- PSG-1503 (wave 15, slice 3): warnings recorded with a completed run, such as
-- REPORT-0015 for PL-FUNC lines without a functional area (FR-15.14).
ALTER TABLE statement_run ADD COLUMN IF NOT EXISTS checks jsonb NOT NULL DEFAULT '[]'::jsonb;
