-- Wave 23 slice 21: resource efficiency (BR-23.1).
--
-- A resource runs at an efficiency in whole percent. A job of N process minutes
-- (route time times quantity) occupies ceil(N x 100 / efficiency_pct) minutes
-- of the resource's open capacity, so a resource at 80 percent takes 25 percent
-- longer than the planned minutes and one at 125 percent takes less. 100 is the
-- default and leaves every existing job as it was placed. No new table: the
-- grants of 0001 (including UPDATE) cover the column.
--
-- scheduling_resource is a live table: the column has a constant default, so
-- adding it is a catalog change (no rewrite), and the check scans a table of a
-- few hundred resources at most under the ACCESS EXCLUSIVE lock. The
-- lock_timeout makes the migration fail (and the rollout retry) instead of
-- queueing behind a long transaction while every later query on the table
-- queues behind it.
SET LOCAL lock_timeout = '5s';

ALTER TABLE scheduling_resource ADD COLUMN IF NOT EXISTS efficiency_pct integer NOT NULL DEFAULT 100;
ALTER TABLE scheduling_resource DROP CONSTRAINT IF EXISTS scheduling_resource_efficiency_check;
ALTER TABLE scheduling_resource ADD CONSTRAINT scheduling_resource_efficiency_check CHECK (efficiency_pct BETWEEN 1 AND 500);
