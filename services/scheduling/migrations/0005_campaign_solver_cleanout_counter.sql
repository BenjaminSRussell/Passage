-- Wave 23 slice 6: CP-SAT campaign sequencing (FR-23.14, BR-23.9) and the
-- cleanout counter a resource carries from one campaign to the next
-- (FR-23.15, BR-23.13).

-- A campaign may ask scheduler-opt for its sequence. The plan records the
-- solver status and whether the solver's order was kept (SCHED-0006).
ALTER TABLE scheduling_campaign DROP CONSTRAINT IF EXISTS scheduling_campaign_sequence_rule_check;
ALTER TABLE scheduling_campaign ADD CONSTRAINT scheduling_campaign_sequence_rule_check
  CHECK (sequence_rule IN ('nearest-neighbor', 'manual', 'cp-sat'));

-- Batches and run minutes a resource has run since its last cleanout. A
-- campaign is sequenced from the counter as it stands and, when it starts,
-- leaves the counter its plan ends with. A start refuses a counter that moved
-- after the campaign was sequenced, so cleanouts are never planned from a
-- stale count.
CREATE TABLE IF NOT EXISTS scheduling_cleanout_counter (
  tenant_id uuid NOT NULL,
  resource_id uuid NOT NULL,
  batches_since integer NOT NULL DEFAULT 0 CHECK (batches_since >= 0),
  minutes_since integer NOT NULL DEFAULT 0 CHECK (minutes_since >= 0),
  last_campaign_id uuid,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, resource_id),
  FOREIGN KEY (tenant_id, resource_id) REFERENCES scheduling_resource (tenant_id, id),
  FOREIGN KEY (tenant_id, last_campaign_id) REFERENCES scheduling_campaign (tenant_id, id)
);
ALTER TABLE scheduling_cleanout_counter ENABLE ROW LEVEL SECURITY;
ALTER TABLE scheduling_cleanout_counter FORCE ROW LEVEL SECURITY;
CREATE POLICY scheduling_cleanout_counter_tenant_access ON scheduling_cleanout_counter AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON scheduling_cleanout_counter TO passage_app';
  END IF;
END
$$;
