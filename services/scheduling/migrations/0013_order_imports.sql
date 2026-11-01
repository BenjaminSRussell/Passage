-- Wave 23 slice 18: production orders received from wave 21 (FR-23.10).
--
-- dev.passage.production.order.scheduled.v1 carries an order's dated jobs on
-- infinite capacity. The scheduling consumer turns each of its operations into
-- a scheduling_operation; scheduling_order_import keeps one row per order with
-- what was done with the latest event (imported, partly imported, rejected)
-- and which operations were left out and why, so a planner can see an order
-- that cannot be scheduled instead of finding it missing.

CREATE TABLE IF NOT EXISTS scheduling_order_import (
  tenant_id uuid NOT NULL,
  order_id uuid NOT NULL,
  order_number text NOT NULL,
  order_version integer NOT NULL CHECK (order_version >= 0),
  reason text NOT NULL CHECK (reason IN ('schedule', 'redate')),
  event_id text NOT NULL,
  status text NOT NULL CHECK (status IN ('imported', 'partial', 'rejected')),
  operations_total integer NOT NULL CHECK (operations_total >= 0),
  created_count integer NOT NULL DEFAULT 0 CHECK (created_count >= 0),
  updated_count integer NOT NULL DEFAULT 0 CHECK (updated_count >= 0),
  unchanged_count integer NOT NULL DEFAULT 0 CHECK (unchanged_count >= 0),
  removed_count integer NOT NULL DEFAULT 0 CHECK (removed_count >= 0),
  kept_firmed_count integer NOT NULL DEFAULT 0 CHECK (kept_firmed_count >= 0),
  skipped jsonb NOT NULL DEFAULT '[]',
  replan_required boolean NOT NULL DEFAULT false,
  order_start timestamptz,
  order_end timestamptz,
  received_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, order_id)
);
CREATE INDEX IF NOT EXISTS scheduling_order_import_status ON scheduling_order_import (tenant_id, status, received_at DESC);
ALTER TABLE scheduling_order_import ENABLE ROW LEVEL SECURITY;
ALTER TABLE scheduling_order_import FORCE ROW LEVEL SECURITY;
CREATE POLICY scheduling_order_import_tenant_access ON scheduling_order_import AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON scheduling_order_import TO passage_app';
  END IF;
END
$$;
