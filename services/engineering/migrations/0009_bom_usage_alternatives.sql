-- FR-9.19 BOM usage and category on the header, FR-9.20 alternative item groups and discontinuation
-- on the line. Existing rows become production material BOMs with no group. No new table, so the
-- passage_app grants from 0001 (SELECT, INSERT, UPDATE, DELETE on bom_version and bom_line) still apply.
ALTER TABLE bom_version ADD COLUMN IF NOT EXISTS usage text NOT NULL DEFAULT 'production';
ALTER TABLE bom_version ADD COLUMN IF NOT EXISTS category text NOT NULL DEFAULT 'material';
ALTER TABLE bom_line ADD COLUMN IF NOT EXISTS alt_group text NOT NULL DEFAULT '';
ALTER TABLE bom_line ADD COLUMN IF NOT EXISTS alt_priority integer;
ALTER TABLE bom_line ADD COLUMN IF NOT EXISTS alt_strategy text NOT NULL DEFAULT '';
ALTER TABLE bom_line ADD COLUMN IF NOT EXISTS usage_probability_pct numeric(7,4);
ALTER TABLE bom_line ADD COLUMN IF NOT EXISTS discontinuation_group text NOT NULL DEFAULT '';
ALTER TABLE bom_line ADD COLUMN IF NOT EXISTS discontinue_type text NOT NULL DEFAULT '';
ALTER TABLE bom_line ADD COLUMN IF NOT EXISTS follow_up_group text NOT NULL DEFAULT '';
ALTER TABLE bom_line ADD COLUMN IF NOT EXISTS follow_up_component_id text NOT NULL DEFAULT '';
ALTER TABLE bom_line ADD COLUMN IF NOT EXISTS discontinue_on date;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'bom_version_usage_check' AND conrelid = 'bom_version'::regclass) THEN
    ALTER TABLE bom_version ADD CONSTRAINT bom_version_usage_check
      CHECK (usage IN ('production', 'engineering', 'costing', 'maintenance', 'sales', 'universal'));
    ALTER TABLE bom_version ADD CONSTRAINT bom_version_category_check
      CHECK (category IN ('material', 'document', 'equipment', 'functional_location', 'sales_order', 'project'));
    ALTER TABLE bom_line ADD CONSTRAINT bom_line_alt_check
      CHECK (alt_strategy IN ('', 'priority', 'usage_probability')
        AND (alt_priority IS NULL OR alt_priority BETWEEN 1 AND 99)
        AND (usage_probability_pct IS NULL OR (usage_probability_pct > 0 AND usage_probability_pct <= 100))
        AND discontinue_type IN ('', 'single', 'simultaneous'));
  END IF;
END
$$;
