-- Wave 21 slice 26: one production order per planned order, whatever path creates it. Since slice 26
-- only POST /v1/production/planned-intake/{id}:convert sets origin PlannedOrder; before it, a plain
-- POST /v1/production/orders could, so a database may already hold two orders naming the same planned
-- order. Kept apart from 0025 so no step here runs under ACCESS EXCLUSIVE.
--
-- The SHARE lock is the one CREATE INDEX takes anyway: reads go on, writes to production_order wait for
-- the check and the build. Taking it first means no order can be inserted between the duplicate check
-- and the build. dbx.Migrate runs every file in a transaction, so CREATE INDEX CONCURRENTLY is not
-- available (NOT-IMPLEMENTED.md, platform handoff). The lock_timeout makes the migration fail (and the
-- rollout retry) instead of queueing behind a long transaction while every later query queues behind it.
SET LOCAL lock_timeout = '5s';

LOCK TABLE production_order IN SHARE MODE;

-- Existing duplicates stop the migration with the planned orders to resolve, instead of the bare
-- unique violation of the build. Nothing is changed automatically: which order is the planned order's
-- and what the others become (for example origin Manual) is a planner's decision.
DO $$
DECLARE
  dup_count integer;
  sample text;
BEGIN
  SELECT count(*), string_agg(tenant_id::text || '/' || origin_id::text, ', ' ORDER BY tenant_id, origin_id) FILTER (WHERE rn <= 10)
    INTO dup_count, sample
    FROM (SELECT tenant_id, origin_id, row_number() OVER (ORDER BY tenant_id, origin_id) AS rn
            FROM production_order
           WHERE origin_type = 'PlannedOrder' AND origin_id IS NOT NULL
           GROUP BY tenant_id, origin_id
          HAVING count(*) > 1) d;
  IF dup_count > 0 THEN
    RAISE EXCEPTION 'production 0026: % planned order(s) have more than one production order with origin PlannedOrder (tenant/planned order, first 10: %)', dup_count, sample
      USING HINT = 'Orders made by POST /v1/production/orders with originType PlannedOrder before slice 26 are not conversions: keep the converted order (its id is the planned order''s target id), set origin_type of the others to Manual, then restart the service.';
  END IF;
END
$$;

CREATE UNIQUE INDEX IF NOT EXISTS production_order_planned_origin
  ON production_order (tenant_id, origin_id) WHERE origin_type = 'PlannedOrder';
