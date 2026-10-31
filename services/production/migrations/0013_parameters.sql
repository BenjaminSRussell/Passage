-- Wave 21 slice 16: production parameters per site (PRD 4.21.13).
-- Additive (expand): a new table only. With no row for a site the service defaults apply.

-- One row per legal entity × site (site_key is a site UUID or 'All'): the tolerances, the
-- auto-start and estimate-on-create switches, the split allocation default, the trigger
-- point switch and the as-built requirement the order rules read.
CREATE TABLE IF NOT EXISTS production_parameters (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  site_key text NOT NULL,
  raf_tolerance_pct numeric(5,2) NOT NULL DEFAULT 0 CHECK (raf_tolerance_pct BETWEEN 0 AND 100),
  over_consumption_pct numeric(6,2) NOT NULL DEFAULT 10 CHECK (over_consumption_pct BETWEEN 0 AND 1000),
  auto_start_on_feedback boolean NOT NULL DEFAULT true,
  estimate_on_create boolean NOT NULL DEFAULT false,
  split_allocation text NOT NULL DEFAULT 'proportional' CHECK (split_allocation IN ('proportional', 'keep-on-original')),
  trigger_points_enabled boolean NOT NULL DEFAULT true,
  as_built_required boolean NOT NULL DEFAULT false,
  version integer NOT NULL DEFAULT 1,
  updated_by text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id, site_key)
);

ALTER TABLE production_parameters ENABLE ROW LEVEL SECURITY;
ALTER TABLE production_parameters FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS production_parameters_tenant_access ON production_parameters;
CREATE POLICY production_parameters_tenant_access ON production_parameters AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
DROP POLICY IF EXISTS production_parameters_legal_entity_scope ON production_parameters;
CREATE POLICY production_parameters_legal_entity_scope ON production_parameters AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- The store inserts and updates (locks) rows, never deletes.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON production_parameters TO passage_app';
  END IF;
END
$$;
