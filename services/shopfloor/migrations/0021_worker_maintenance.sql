-- PSG-25 slice 22: worker maintenance (D2a'). A supervisor edits a worker's own fields and
-- deactivates a worker who left (with a reason) or reactivates one who returned. The three
-- columns record the latest deactivation; they are null for an active worker. `worker` already
-- carries tenant_id with forced row-level security and is granted to the application role
-- (SELECT, INSERT, UPDATE) by earlier migrations, so no new grant is needed.
--
-- worker is a live table: nullable columns without a default only change the catalog, so the
-- ACCESS EXCLUSIVE lock lasts an instant and nothing is scanned or rewritten. The lock_timeout
-- makes the migration fail (and the rollout retry) instead of queueing behind a long transaction.
SET LOCAL lock_timeout = '5s';

ALTER TABLE worker ADD COLUMN IF NOT EXISTS deactivated_at timestamptz;
ALTER TABLE worker ADD COLUMN IF NOT EXISTS deactivated_by text;
ALTER TABLE worker ADD COLUMN IF NOT EXISTS deactivation_reason text CHECK (char_length(deactivation_reason) BETWEEN 1 AND 500);
