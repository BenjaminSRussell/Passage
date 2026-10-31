-- FR-22.15, BR-22.14: planning scenarios and simulation plan versions.
--
-- planning_scenario is the ScenarioOverride of the data model: a named set of
-- changes (a demand factor overall and per item, added demand lines, lead-time
-- overrides) that a simulation run applies to the snapshot it is given. It
-- belongs to one plan (plan_id, with plan_code kept for reads; a plan's code
-- never changes) and is entity-specific like the plan: legal_entity_id is the
-- plan's owner, copied when the scenario is created, so a caller outside that
-- entity can neither read, change nor run it. At most 10 scenarios per plan
-- are active.
CREATE TABLE IF NOT EXISTS planning_scenario (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{1,19}$'),
  plan_id uuid NOT NULL,
  plan_code text NOT NULL CHECK (plan_code ~ '^[A-Z0-9][A-Z0-9_-]{1,19}$'),
  legal_entity_id uuid,
  name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
  demand_factor numeric(10,4) NOT NULL DEFAULT 1 CHECK (demand_factor >= 0 AND demand_factor <= 100),
  item_factors jsonb NOT NULL DEFAULT '[]'::jsonb,
  added_demand jsonb NOT NULL DEFAULT '[]'::jsonb,
  lead_time_overrides jsonb NOT NULL DEFAULT '[]'::jsonb,
  active boolean NOT NULL DEFAULT true,
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  idempotency_key text,
  input_hash text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, plan_id) REFERENCES plan (tenant_id, id),
  CONSTRAINT planning_scenario_code_key UNIQUE (tenant_id, code),
  CONSTRAINT planning_scenario_idempotency_key UNIQUE (tenant_id, idempotency_key),
  CHECK ((idempotency_key IS NULL) = (input_hash IS NULL))
);
CREATE INDEX IF NOT EXISTS planning_scenario_plan ON planning_scenario (tenant_id, plan_id) WHERE active;
ALTER TABLE planning_scenario ENABLE ROW LEVEL SECURITY;
ALTER TABLE planning_scenario FORCE ROW LEVEL SECURITY;
CREATE POLICY planning_scenario_tenant_access ON planning_scenario AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY planning_scenario_legal_entity_scope ON planning_scenario AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- A simulation run writes its own plan version of kind 'simulation'. It is
-- never current (the operational pointer is untouched, BR-22.14) and its
-- planned orders cannot be firmed.
ALTER TABLE plan_version DROP CONSTRAINT IF EXISTS plan_version_kind_check;
ALTER TABLE plan_version ADD CONSTRAINT plan_version_kind_check CHECK (kind IN ('static', 'dynamic', 'simulation'));
ALTER TABLE plan_version ADD COLUMN IF NOT EXISTS scenario_id uuid;
ALTER TABLE plan_version DROP CONSTRAINT IF EXISTS plan_version_scenario_fk;
ALTER TABLE plan_version ADD CONSTRAINT plan_version_scenario_fk FOREIGN KEY (tenant_id, scenario_id) REFERENCES planning_scenario (tenant_id, id);
ALTER TABLE plan_version DROP CONSTRAINT IF EXISTS plan_version_simulation_check;
ALTER TABLE plan_version ADD CONSTRAINT plan_version_simulation_check CHECK ((kind = 'simulation') = (scenario_id IS NOT NULL) AND (kind <> 'simulation' OR status = 'complete'));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON planning_scenario TO passage_app';
  END IF;
END
$$;
