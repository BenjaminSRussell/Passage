-- Wave 23 slice 36: manual reset of a resource's cleanout counter (FR-23.15,
-- BR-23.13).
--
-- The counter (scheduling_cleanout_counter, migration 0005) moves only when a
-- campaign starts or is cancelled. A cleanout done outside the plan (an
-- unplanned wash-down, a changed product) has to be told to the scheduler by a
-- planner, so the next campaign is sequenced from a clean resource. Every
-- reset is kept: the counts the resource carried, who reset it, why.

CREATE TABLE IF NOT EXISTS scheduling_cleanout_reset (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  resource_id uuid NOT NULL,
  previous_batches integer NOT NULL CHECK (previous_batches >= 0),
  previous_minutes integer NOT NULL CHECK (previous_minutes >= 0),
  reason text NOT NULL CHECK (char_length(btrim(reason)) BETWEEN 1 AND 200),
  reset_by text NOT NULL DEFAULT '',
  -- Planned campaigns whose plan was sequenced from the cleared counter; they
  -- refuse to start (SCHED-0123) until sequenced again. Kept so a replay of
  -- the reset answers the same list.
  stale_campaign_ids uuid[] NOT NULL DEFAULT '{}',
  idempotency_key text,
  idempotency_fingerprint text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, resource_id) REFERENCES scheduling_resource (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS scheduling_cleanout_reset_resource ON scheduling_cleanout_reset (tenant_id, resource_id, created_at DESC);
ALTER TABLE scheduling_cleanout_reset ENABLE ROW LEVEL SECURITY;
ALTER TABLE scheduling_cleanout_reset FORCE ROW LEVEL SECURITY;
CREATE POLICY scheduling_cleanout_reset_tenant_access ON scheduling_cleanout_reset AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON scheduling_cleanout_reset TO passage_app';
  END IF;
END
$$;
