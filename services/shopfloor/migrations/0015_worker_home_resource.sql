-- PSG-25 slice 16: a worker's home resource (D2a'). The dispatch list the PRD calls "my jobs"
-- (GET jobs?workerId=) is the jobs of the resource the worker normally runs, and the kiosk shows
-- it for the signed-in worker. The column is a resource id from engineering data (w9), another
-- service, so there is no foreign key; the store accepts only a resource this tenant's shop floor
-- knows (a registered machine's resource or one with dispatch jobs, SF-0156). Null means the worker
-- has no home resource and sees every job. `worker` already carries tenant_id with forced
-- row-level security and is granted to the application role (SELECT, INSERT, UPDATE) by earlier
-- migrations, so no new grant is needed.
--
-- worker is a live table: a nullable column without a default only changes the catalog, so its
-- ACCESS EXCLUSIVE lock lasts an instant and nothing is scanned or rewritten. The lock_timeout
-- makes the migration fail (and the rollout retry) instead of queueing behind a long transaction
-- while every later query on worker queues behind it. No index: nothing looks workers up by it.
SET LOCAL lock_timeout = '5s';

ALTER TABLE worker ADD COLUMN IF NOT EXISTS default_resource_id uuid;
