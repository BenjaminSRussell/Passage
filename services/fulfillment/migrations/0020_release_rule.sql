-- Fulfillment slice 17: the release and wave parameters per warehouse (section
-- 5.28.13: release_horizon_days, release_requires_full_atp, wave_max_lines,
-- wave_max_orders). A release run that does not name a horizon or the ATP rule,
-- and a picking workbench call that does not name wave limits, take the saved
-- rule of the warehouse. A warehouse belongs to one legal entity, so the rule
-- carries it and is scoped by it (the shape of warehouse_setting).
CREATE TABLE IF NOT EXISTS warehouse_release_rule (
  tenant_id uuid NOT NULL,
  warehouse_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  release_horizon_days integer NOT NULL DEFAULT 2 CHECK (release_horizon_days BETWEEN 0 AND 30),
  release_requires_full_atp boolean NOT NULL DEFAULT true,
  wave_max_lines integer NOT NULL DEFAULT 200 CHECK (wave_max_lines BETWEEN 1 AND 2000),
  wave_max_orders integer NOT NULL DEFAULT 50 CHECK (wave_max_orders BETWEEN 1 AND 500),
  version integer NOT NULL DEFAULT 1,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, warehouse_id)
);

ALTER TABLE warehouse_release_rule ENABLE ROW LEVEL SECURITY;
ALTER TABLE warehouse_release_rule FORCE ROW LEVEL SECURITY;
CREATE POLICY warehouse_release_rule_tenant_access ON warehouse_release_rule AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY warehouse_release_rule_legal_entity_scope ON warehouse_release_rule AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON warehouse_release_rule TO passage_app';
  END IF;
END
$$;
