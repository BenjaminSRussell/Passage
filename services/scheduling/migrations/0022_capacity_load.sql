-- Wave 23 slice 30: materialized weekly capacity load and bottleneck episodes
-- (FR-23.9, AC-23.6, SCHED-0015).
--
-- scheduling_capacity_load holds, per resource and UTC week (Monday 00:00), the
-- booked and open minutes and the bottleneck flag, for every week that has any
-- booking. The service rewrites a resource's rows in the same transaction that
-- places, moves or removes its jobs, or changes its calendar or overrides, so
-- the table is never behind committed bookings; a week with no row is booked
-- 0. The flag is the one capacity-load reports: load above the configured
-- percentage for the configured number of consecutive weeks.
--
-- scheduling_bottleneck_episode records a stretch during which a resource has
-- at least one flagged week from now on. bottleneck.detected is written when an
-- episode opens, so a resource that stays overloaded across many runs is
-- reported once; the episode closes when no flagged week remains. At most one
-- episode per resource is open. last_week is its last flagged week at the
-- latest rewrite: once that week is past the episode is over even if nothing
-- touched the resource since (reads report it closed; the next rewrite closes
-- it as of the end of that week before it opens a new one).
--
-- Neither table has a foreign key to scheduling_resource (resources are never
-- deleted). The rows are rewritten at the end of a writer's transaction under
-- the resource's load advisory lock, and the key-share lock a foreign key
-- check takes on the resource row would wait on UpdateResource, which holds
-- that row FOR UPDATE and then waits for the same advisory lock: a deadlock.
SET LOCAL lock_timeout = '5s';

CREATE TABLE IF NOT EXISTS scheduling_capacity_load (
  tenant_id uuid NOT NULL,
  resource_id uuid NOT NULL,
  week_start date NOT NULL,
  open_minutes integer NOT NULL CHECK (open_minutes >= 0),
  booked_minutes integer NOT NULL CHECK (booked_minutes > 0),
  load_pct numeric(8, 1) NOT NULL CHECK (load_pct >= 0),
  bottleneck boolean NOT NULL DEFAULT false,
  refreshed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, resource_id, week_start)
);
CREATE INDEX IF NOT EXISTS scheduling_capacity_load_flagged
  ON scheduling_capacity_load (tenant_id, week_start, resource_id) WHERE bottleneck;
ALTER TABLE scheduling_capacity_load ENABLE ROW LEVEL SECURITY;
ALTER TABLE scheduling_capacity_load FORCE ROW LEVEL SECURITY;
CREATE POLICY scheduling_capacity_load_tenant_access ON scheduling_capacity_load AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS scheduling_bottleneck_episode (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  resource_id uuid NOT NULL,
  first_week date NOT NULL,
  last_week date NOT NULL,
  flagged_weeks integer NOT NULL CHECK (flagged_weeks >= 1),
  peak_load_pct numeric(8, 1) NOT NULL,
  threshold_pct integer NOT NULL,
  consecutive_weeks integer NOT NULL,
  detected_at timestamptz NOT NULL DEFAULT now(),
  closed_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  CHECK (last_week >= first_week)
);
CREATE UNIQUE INDEX IF NOT EXISTS scheduling_bottleneck_episode_open
  ON scheduling_bottleneck_episode (tenant_id, resource_id) WHERE closed_at IS NULL;
CREATE INDEX IF NOT EXISTS scheduling_bottleneck_episode_detected
  ON scheduling_bottleneck_episode (tenant_id, detected_at DESC);
ALTER TABLE scheduling_bottleneck_episode ENABLE ROW LEVEL SECURITY;
ALTER TABLE scheduling_bottleneck_episode FORCE ROW LEVEL SECURITY;
CREATE POLICY scheduling_bottleneck_episode_tenant_access ON scheduling_bottleneck_episode AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON scheduling_capacity_load TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON scheduling_bottleneck_episode TO passage_app';
  END IF;
END
$$;
