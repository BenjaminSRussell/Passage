-- Wave 23 slice 37: a sequenced campaign placed on its resource (FR-23.14).
--
-- :place lays the campaign's orders on the Gantt back to back in plan
-- sequence with the changeovers of the setup matrix and the plan's cleanouts.
-- The orders become ordinary scheduled jobs; each cleanout is a closed window
-- on the resource that no run, move or inquiry treats as open capacity. The
-- window is owned by the campaign, so unplacing, re-placing or cancelling the
-- campaign removes it.

ALTER TABLE schedule_run DROP CONSTRAINT IF EXISTS schedule_run_scope_check;
ALTER TABLE schedule_run ADD CONSTRAINT schedule_run_scope_check
  CHECK (scope IN ('order', 'selection', 'site', 'move', 'rollback', 'campaign'));

-- placed_at and placed_run_id say the campaign holds a placement and which run
-- wrote it; planned_end (column of 0002) is the end of its last window.
-- operation_id is the operation of each campaign order that the placement
-- booked.
ALTER TABLE scheduling_campaign
  ADD COLUMN IF NOT EXISTS placed_at timestamptz,
  ADD COLUMN IF NOT EXISTS placed_run_id uuid;
ALTER TABLE scheduling_campaign_order
  ADD COLUMN IF NOT EXISTS operation_id uuid;

CREATE TABLE IF NOT EXISTS scheduling_campaign_cleanout (
  tenant_id uuid NOT NULL,
  campaign_id uuid NOT NULL,
  after_order_id uuid NOT NULL,
  resource_id uuid NOT NULL,
  start_at timestamptz NOT NULL,
  end_at timestamptz NOT NULL,
  minutes integer NOT NULL CHECK (minutes > 0),
  setup_group text NOT NULL,
  reason text NOT NULL,
  cleaning_operation_set_id text,
  PRIMARY KEY (tenant_id, campaign_id, after_order_id),
  CHECK (end_at > start_at),
  FOREIGN KEY (tenant_id, campaign_id) REFERENCES scheduling_campaign (tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, resource_id) REFERENCES scheduling_resource (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS scheduling_campaign_cleanout_resource ON scheduling_campaign_cleanout (tenant_id, resource_id, start_at);
ALTER TABLE scheduling_campaign_cleanout ENABLE ROW LEVEL SECURITY;
ALTER TABLE scheduling_campaign_cleanout FORCE ROW LEVEL SECURITY;
CREATE POLICY scheduling_campaign_cleanout_tenant_access ON scheduling_campaign_cleanout AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON scheduling_campaign_cleanout TO passage_app';
  END IF;
END
$$;
