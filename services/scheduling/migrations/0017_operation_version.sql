-- Wave 23 slice 23: operation maintenance (FR-23.4, FR-23.11).
--
-- A planner changes an operation's duration, dates, priority or candidate
-- resources, removes an operation, or takes an order off the schedule. Changes
-- use optimistic versioning like resources and campaigns, so an operation gets
-- a version (1 when created) and the time of its last change. No new table:
-- the UPDATE and DELETE grants of 0001 cover scheduling_operation.
SET LOCAL lock_timeout = '5s';

ALTER TABLE scheduling_operation ADD COLUMN IF NOT EXISTS version integer NOT NULL DEFAULT 1 CHECK (version >= 1);
ALTER TABLE scheduling_operation ADD COLUMN IF NOT EXISTS updated_at timestamptz;
