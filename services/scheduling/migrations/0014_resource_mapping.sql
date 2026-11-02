-- Wave 23 slice 19: production (engineering) resource ids mapped to scheduling
-- resources, and re-import of a received production order.
--
-- Production's order.scheduled.v1 names the engineering (wave 09) resource of
-- each job. Scheduling resources carry their own ids, so a resource can hold
-- the id of the engineering resource it stands for; the order intake resolves a
-- job's resource through that mapping first and by scheduling id second. At most
-- one scheduling resource stands for an engineering resource in a tenant.
--
-- scheduling_order_import keeps the order as it was received (source) so that a
-- planner who has just mapped a resource can import the order again without
-- waiting for production to send a new event. Rows received before this
-- migration have no source and wait for their next event.

ALTER TABLE scheduling_resource ADD COLUMN IF NOT EXISTS engineering_resource_id uuid;
CREATE UNIQUE INDEX IF NOT EXISTS scheduling_resource_engineering_id
  ON scheduling_resource (tenant_id, engineering_resource_id) WHERE engineering_resource_id IS NOT NULL;

ALTER TABLE scheduling_order_import ADD COLUMN IF NOT EXISTS source jsonb;
