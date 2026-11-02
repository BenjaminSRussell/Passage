-- Second half of 0020, kept apart so no step here runs under ACCESS EXCLUSIVE:
-- VALIDATE CONSTRAINT takes SHARE UPDATE EXCLUSIVE (reads and writes go on),
-- the backfill takes row locks on the few flagged lines, and CREATE INDEX takes
-- SHARE (reads go on; writes to count_sheet_line wait for the build). dbx.Migrate
-- runs every file in a transaction, so CREATE INDEX CONCURRENTLY is not
-- available (NOT-IMPLEMENTED.md, platform handoff).
SET LOCAL lock_timeout = '5s';

ALTER TABLE count_plan VALIDATE CONSTRAINT count_plan_min_days_since_count_check;
ALTER TABLE count_sheet_line VALIDATE CONSTRAINT count_sheet_line_recount_source_check;

-- Every recount flagged before 0020 came from a supervisor.
UPDATE count_sheet_line SET recount_source = 'SUPERVISOR' WHERE recount_requested AND recount_source IS NULL;

-- The last-count-age filter reads the counted time of lines on POSTED sheets.
CREATE INDEX IF NOT EXISTS count_sheet_line_counted_idx ON count_sheet_line (tenant_id, product_id, dimension_set_hash, counted_at) WHERE counted_qty IS NOT NULL;
