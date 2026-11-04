-- PSG-25 slice 23: idle time as a DowntimeRecord (FR-25.11, BR-25.6, D4). A RUNNING machine that
-- sends no count for its idle threshold is idle, and the scheduled andon sweep now opens a
-- downtime for it (an idle machine sends no event that could). `cause` tells that record from a
-- downtime the machine reported (DOWN); a count or a state that leaves RUNNING (DOWN, SETUP,
-- OFF) ends an idle record.
-- downtime_record already carries tenant_id with forced row-level security and is granted to the
-- application role (SELECT, INSERT, UPDATE, DELETE) by migration 0002, so no new grant is needed.
--
-- downtime_record is a live table: adding a column with a constant default changes only the
-- catalog, so the ACCESS EXCLUSIVE lock lasts an instant. The lock_timeout makes the migration
-- fail (and the rollout retry) instead of queueing behind a long transaction.
SET LOCAL lock_timeout = '5s';

ALTER TABLE downtime_record ADD COLUMN IF NOT EXISTS cause text NOT NULL DEFAULT 'down' CHECK (cause IN ('down', 'idle'));
