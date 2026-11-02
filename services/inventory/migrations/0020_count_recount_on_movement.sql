-- Cycle-count follow-up (FR-16.9, BR-16.8): a plan without a freeze flags the
-- lines a later movement touched for recount, and a plan can select only rows
-- not counted for a number of days. No new tables: the count tables already
-- carry the passage_app grants (SELECT, INSERT, UPDATE) from 0003.
--
-- count_plan and count_sheet_line are live tables, so this file only changes
-- the catalog: nullable columns without a default and NOT VALID checks take
-- their ACCESS EXCLUSIVE lock for an instant and never scan or rewrite. The
-- lock_timeout makes the migration fail (and the rollout retry) instead of
-- queueing behind a long transaction while every later query queues behind
-- it. 0021 validates the checks, backfills and builds the index without that
-- lock.
SET LOCAL lock_timeout = '5s';

ALTER TABLE count_plan ADD COLUMN IF NOT EXISTS min_days_since_count integer;
ALTER TABLE count_plan ADD CONSTRAINT count_plan_min_days_since_count_check
  CHECK (min_days_since_count IS NULL OR min_days_since_count BETWEEN 1 AND 3650) NOT VALID;

-- Who asked for the recount: a supervisor, or the system because stock moved
-- after the line was counted.
ALTER TABLE count_sheet_line ADD COLUMN IF NOT EXISTS recount_source text;
ALTER TABLE count_sheet_line ADD CONSTRAINT count_sheet_line_recount_source_check
  CHECK (recount_source IS NULL OR recount_source IN ('SUPERVISOR', 'MOVEMENT')) NOT VALID;
