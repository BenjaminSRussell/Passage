-- Wave 21 slice 26: a production order converted from a planned order keeps the quota arrangement and
-- source planning allocated it to (FR-22.17). Additive (expand). No new table, so no new grant:
-- production_order is granted in 0001.
--
-- production_order is a live table, so this file only changes the catalog: nullable columns without a
-- default take their ACCESS EXCLUSIVE lock for an instant and never scan or rewrite. The lock_timeout
-- makes the migration fail (and the rollout retry) instead of queueing behind a long transaction while
-- every later query on the table queues behind it. 0026 builds the unique index without that lock.
SET LOCAL lock_timeout = '5s';

ALTER TABLE production_order ADD COLUMN IF NOT EXISTS quota_arrangement_id uuid;
ALTER TABLE production_order ADD COLUMN IF NOT EXISTS quota_source_id uuid;
