-- PSG-1507 (wave 15, slice 7): statement runs over a derived hierarchy (FR-15.4,
-- FR-15.13). A run that takes its rows from a hierarchy records the hierarchy
-- version it used and the expanded definition it evaluated (one row per node), so
-- the statement page, the downloads and drill level 2 show exactly those rows
-- later, whatever happens to the hierarchy. Plain runs leave all three NULL.
-- statement_run keeps the grants and policies it already has.
ALTER TABLE statement_run ADD COLUMN IF NOT EXISTS hierarchy_code text;
ALTER TABLE statement_run ADD COLUMN IF NOT EXISTS hierarchy_version integer;
ALTER TABLE statement_run ADD COLUMN IF NOT EXISTS run_definition jsonb;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'statement_run_hierarchy_complete') THEN
    ALTER TABLE statement_run ADD CONSTRAINT statement_run_hierarchy_complete
      CHECK ((hierarchy_code IS NULL) = (hierarchy_version IS NULL) AND (hierarchy_code IS NULL) = (run_definition IS NULL));
  END IF;
END
$$;
