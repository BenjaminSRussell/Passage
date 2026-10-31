-- Cycle-count control (FR-16.9): plan selection (location range, ABC class,
-- sample percent), stock freeze while a sheet is open (INV-CNT-002), recount
-- flags, and the COUNTED / APPROVED / CANCELLED sheet statuses with an
-- optional four-eyes rule. No new tables: the existing count tables already
-- carry the passage_app grants (SELECT, INSERT, UPDATE) from 0003.
ALTER TABLE count_plan
  ADD COLUMN IF NOT EXISTS freeze_stock boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS four_eyes boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS location_from text CHECK (location_from IS NULL OR char_length(location_from) BETWEEN 1 AND 60),
  ADD COLUMN IF NOT EXISTS location_to text CHECK (location_to IS NULL OR char_length(location_to) BETWEEN 1 AND 60),
  ADD COLUMN IF NOT EXISTS abc_class text CHECK (abc_class IS NULL OR abc_class IN ('A', 'B', 'C')),
  ADD COLUMN IF NOT EXISTS sample_pct numeric(9,6) CHECK (sample_pct IS NULL OR (sample_pct > 0 AND sample_pct <= 100));

ALTER TABLE count_sheet DROP CONSTRAINT IF EXISTS count_sheet_status_check;
ALTER TABLE count_sheet ADD CONSTRAINT count_sheet_status_check
  CHECK (status IN ('OPEN', 'COUNTED', 'APPROVED', 'POSTED', 'CANCELLED'));
ALTER TABLE count_sheet
  ADD COLUMN IF NOT EXISTS submitted_by text,
  ADD COLUMN IF NOT EXISTS submitted_at timestamptz,
  ADD COLUMN IF NOT EXISTS approved_at timestamptz,
  ADD COLUMN IF NOT EXISTS cancelled_by text,
  ADD COLUMN IF NOT EXISTS cancelled_at timestamptz;

ALTER TABLE count_sheet_line
  ADD COLUMN IF NOT EXISTS recount_requested boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS recount_reason text CHECK (recount_reason IS NULL OR char_length(recount_reason) BETWEEN 1 AND 500),
  ADD COLUMN IF NOT EXISTS recount_by text,
  ADD COLUMN IF NOT EXISTS recount_rounds integer NOT NULL DEFAULT 0;

-- The freeze check looks up open sheet lines by product and dimension set.
CREATE INDEX IF NOT EXISTS count_sheet_line_row_idx ON count_sheet_line (tenant_id, product_id, dimension_set_hash);
