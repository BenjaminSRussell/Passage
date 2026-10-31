-- Wave assignment and close (FR-28.13, section 5.28.6). A wave gets a picker, which a
-- supervisor may change while the wave is OPEN, and is CLOSED once every line of it is
-- picked or its shipment cancelled. The wave row is now updated (picker, status,
-- version), so passage_app also needs UPDATE on it. No new table.
ALTER TABLE wave ADD COLUMN IF NOT EXISTS picker text CHECK (picker IS NULL OR char_length(picker) BETWEEN 1 AND 64);
ALTER TABLE wave ADD COLUMN IF NOT EXISTS assigned_at timestamptz;
ALTER TABLE wave ADD COLUMN IF NOT EXISTS closed_at timestamptz;
ALTER TABLE wave ADD COLUMN IF NOT EXISTS version integer NOT NULL DEFAULT 1;
ALTER TABLE wave ADD COLUMN IF NOT EXISTS updated_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE wave DROP CONSTRAINT IF EXISTS wave_status_check;
ALTER TABLE wave ADD CONSTRAINT wave_status_check CHECK (status IN ('OPEN', 'CLOSED'));
CREATE INDEX IF NOT EXISTS wave_picker_idx ON wave (tenant_id, picker, status) WHERE picker IS NOT NULL;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON wave TO passage_app';
  END IF;
END
$$;
