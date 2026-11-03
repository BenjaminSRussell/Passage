-- FR-22.18: net requirement rows are unique per item, scope and day. The key of 0004
-- (tenant, version, item, day) would reject the site row and the warehouse row of one
-- item on one day, so this builds its successor with the scope in it; NULLS NOT DISTINCT
-- keeps exactly one site-scope (null warehouse) row per item and day, as before. The
-- index serves every read the old key served: the version purge by (tenant, version),
-- the site read (warehouse_id IS NULL is an index condition) and the warehouse read.
-- Nothing writes net_requirement with ON CONFLICT. CREATE INDEX takes SHARE: reads go
-- on, a plan run writing the table waits for the build. dbx.Migrate runs each file in a
-- transaction, so CONCURRENTLY is not available (as in inventory 0021); the old key is
-- dropped in 0025 so no ACCESS EXCLUSIVE lock is held while this builds.
SET LOCAL lock_timeout = '5s';

CREATE UNIQUE INDEX IF NOT EXISTS net_requirement_scope_day
  ON net_requirement (tenant_id, plan_version_id, item_id, warehouse_id, day) NULLS NOT DISTINCT;

-- GET /planned-orders?warehouseId= within a version; only warehouse-scope orders are indexed.
CREATE INDEX IF NOT EXISTS planned_order_warehouse ON planned_order (tenant_id, plan_version_id, warehouse_id)
  WHERE warehouse_id IS NOT NULL;
