-- FR-22.18: drop the 0004 key (tenant, version, item, day), which 0024's
-- net_requirement_scope_day replaces, so a site row and a warehouse row of one item can
-- share a day. Dropping a constraint is catalog-only but takes ACCESS EXCLUSIVE for an
-- instant; it is kept apart from the index build of 0024 so that lock is never held
-- while an index builds.
SET LOCAL lock_timeout = '5s';

ALTER TABLE net_requirement DROP CONSTRAINT IF EXISTS net_requirement_tenant_id_plan_version_id_item_id_day_key;
