-- Slice 5 (PSG-4105): the common ancestor of a SIMULATION for the three-way
-- transfer into WORKING (FR-41.17, BR-41.14), and forecast models with
-- forecast-vs-actual (FR-41.20). Additive only.

-- base_fingerprints is {wbs_code: fingerprint} of the version a SIMULATION was
-- copied from, taken in the copy's transaction (domain.Fingerprints). The source
-- WORKING plan keeps changing, so the ancestor must be recorded, not re-read.
-- NULL on simulations copied before this migration; those cannot be transferred.
ALTER TABLE plan_version ADD COLUMN IF NOT EXISTS base_fingerprints jsonb;
ALTER TABLE plan_version ADD COLUMN IF NOT EXISTS transferred_at timestamptz;
ALTER TABLE plan_version ADD CONSTRAINT plan_version_base_simulation_only
  CHECK (base_fingerprints IS NULL OR kind = 'SIMULATION');
ALTER TABLE plan_version ADD CONSTRAINT plan_version_transferred_locked
  CHECK (transferred_at IS NULL OR (kind = 'SIMULATION' AND locked));

-- ProjectForecast (§8.41.4). A model groups forecast lines; currency is the
-- project currency at creation (a project's currency does not change).
CREATE TABLE IF NOT EXISTS project_forecast_model (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  project_id text NOT NULL,
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 80),
  currency char(3) NOT NULL,
  created_by text NOT NULL CHECK (char_length(created_by) >= 1),
  version integer NOT NULL DEFAULT 1,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, project_id) REFERENCES project (tenant_id, id),
  UNIQUE (tenant_id, project_id, name),
  UNIQUE (tenant_id, idempotency_key)
);
ALTER TABLE project_forecast_model ENABLE ROW LEVEL SECURITY;
ALTER TABLE project_forecast_model FORCE ROW LEVEL SECURITY;
CREATE POLICY project_forecast_model_tenant_access ON project_forecast_model AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
-- Legal-entity scope through the owning project (itself scoped by RLS).
CREATE POLICY project_forecast_model_legal_entity_scope ON project_forecast_model AS RESTRICTIVE
  USING (EXISTS (SELECT 1 FROM project p WHERE p.tenant_id = project_forecast_model.tenant_id AND p.id = project_forecast_model.project_id));

-- Forecast lines: period is a calendar month until wave-14 fiscal periods
-- exist; the node is kept by WBS code (as on project_transaction). Amounts are
-- rounded to the currency's minor unit by the service.
CREATE TABLE IF NOT EXISTS project_forecast_line (
  tenant_id uuid NOT NULL,
  model_id uuid NOT NULL,
  line_no integer NOT NULL CHECK (line_no >= 1),
  period text NOT NULL CHECK (period ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
  wbs_code text CHECK (wbs_code IS NULL OR char_length(wbs_code) BETWEEN 1 AND 40),
  txn_type text NOT NULL CHECK (txn_type IN ('HOUR', 'COST', 'ITEM', 'FEE', 'ON_ACCOUNT')),
  quantity numeric(14,3) NOT NULL CHECK (quantity >= 0),
  cost_amount numeric(20,6) NOT NULL CHECK (cost_amount >= 0),
  sales_amount numeric(20,6) NOT NULL,
  PRIMARY KEY (tenant_id, model_id, line_no),
  FOREIGN KEY (tenant_id, model_id) REFERENCES project_forecast_model (tenant_id, id),
  CHECK (sales_amount >= 0 OR txn_type = 'ON_ACCOUNT')
);
CREATE UNIQUE INDEX IF NOT EXISTS project_forecast_line_key ON project_forecast_line
  (tenant_id, model_id, period, txn_type, coalesce(wbs_code, ''));
ALTER TABLE project_forecast_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE project_forecast_line FORCE ROW LEVEL SECURITY;
CREATE POLICY project_forecast_line_tenant_access ON project_forecast_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY project_forecast_line_legal_entity_scope ON project_forecast_line AS RESTRICTIVE
  USING (EXISTS (SELECT 1 FROM project_forecast_model m
    WHERE m.tenant_id = project_forecast_line.tenant_id AND m.id = project_forecast_line.model_id));

-- Application role privileges for the slice-5 tables (see 0005_grants.sql). The
-- model row is updated and locked FOR UPDATE on replace; replacing the lines
-- deletes them. Skipped when the role does not exist (developer databases
-- without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON project_forecast_model TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON project_forecast_line TO passage_app';
  END IF;
END
$$;
