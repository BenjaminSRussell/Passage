-- Wave 21 slice 24: trigger points evaluated when an operation finishes (FR-21.17).
-- Additive (expand): a defaulted column on the trigger point and a firing that may name no journal.
-- No new table, so no new grant: both tables are granted in 0006.
--
-- Both tables are live. A column with a constant default, dropping NOT NULL and NOT VALID
-- checks only change the catalog, so the ACCESS EXCLUSIVE lock lasts an instant and nothing is
-- scanned or rewritten. The lock_timeout makes the migration fail (and the rollout retry)
-- instead of queueing behind a long transaction while every later query on the tables queues
-- behind it. 0023 validates the checks without that lock. Every existing row already satisfies
-- them (event takes its default and every firing so far has a journal).
SET LOCAL lock_timeout = '5s';

-- feedback: evaluated on each route card or job card line of the operation (every earlier
-- trigger point). operation-finished: evaluated when the operation becomes finished, against
-- the hours and quantities posted on it.
ALTER TABLE production_trigger_point ADD COLUMN IF NOT EXISTS event text NOT NULL DEFAULT 'feedback';
ALTER TABLE production_trigger_point DROP CONSTRAINT IF EXISTS production_trigger_point_event_check;
ALTER TABLE production_trigger_point ADD CONSTRAINT production_trigger_point_event_check
  CHECK (event IN ('feedback', 'operation-finished')) NOT VALID;

-- A firing on an operation finishing has no journal: the :finish command is not one. The
-- firing's event says which kind it is; a feedback firing keeps its journal.
ALTER TABLE production_trigger_firing ALTER COLUMN journal_id DROP NOT NULL;
ALTER TABLE production_trigger_firing ADD COLUMN IF NOT EXISTS event text NOT NULL DEFAULT 'feedback';
ALTER TABLE production_trigger_firing DROP CONSTRAINT IF EXISTS production_trigger_firing_event_check;
ALTER TABLE production_trigger_firing ADD CONSTRAINT production_trigger_firing_event_check
  CHECK (event IN ('feedback', 'operation-finished')) NOT VALID;
ALTER TABLE production_trigger_firing DROP CONSTRAINT IF EXISTS production_trigger_firing_event_journal;
ALTER TABLE production_trigger_firing ADD CONSTRAINT production_trigger_firing_event_journal
  CHECK ((event = 'feedback') = (journal_id IS NOT NULL)) NOT VALID;
