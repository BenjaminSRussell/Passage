-- FR-40.9, FR-40.16, FR-40.18: what a run over a stored collection needs for dividends (E6), IC inventory layers and the equity method.
-- A mapping can name the role of the account a unit books its declared dividends on; a unit's terms say whether the group consolidates
-- it line by line or takes it with the equity method; the IC receipt layers a subsidiary's goods still sit in are stored per period;
-- and every run keeps the investments register it computed.

ALTER TABLE consolidation_account_map DROP CONSTRAINT IF EXISTS consolidation_account_map_role_check;
ALTER TABLE consolidation_account_map ADD CONSTRAINT consolidation_account_map_role_check
  CHECK (role IS NULL OR role IN ('asset', 'liability', 'equity', 'ic_sales', 'ic_cost', 'ic_receivable', 'ic_payable', 'dividend_paid'));

ALTER TABLE consolidation_unit_terms ADD COLUMN IF NOT EXISTS method text NOT NULL DEFAULT 'full' CHECK (method IN ('full', 'equity'));

CREATE TABLE IF NOT EXISTS consolidation_ic_layer (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  unit_id uuid NOT NULL,
  period text NOT NULL CHECK (period ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
  ref text NOT NULL CHECK (length(ref) BETWEEN 1 AND 128),
  quantity numeric(28,6) NOT NULL CHECK (quantity >= 0),
  legal_unit_cost numeric(28,6) NOT NULL CHECK (legal_unit_cost >= 0),
  group_unit_cost numeric(28,6) CHECK (group_unit_cost IS NULL OR group_unit_cost >= 0),
  updated_by text NOT NULL CHECK (length(updated_by) >= 1),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, unit_id, period, ref),
  FOREIGN KEY (tenant_id, unit_id) REFERENCES consolidation_unit (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS consolidation_investment (
  tenant_id uuid NOT NULL,
  run_id uuid NOT NULL,
  unit_id text NOT NULL CHECK (length(unit_id) BETWEEN 1 AND 64),
  method text NOT NULL CHECK (method IN ('full', 'equity')),
  ownership_pct numeric(9,4) NOT NULL,
  cost numeric(28,6) NOT NULL,
  opening_carrying numeric(28,6) NOT NULL,
  share_of_profit numeric(28,6) NOT NULL,
  dividends numeric(28,6) NOT NULL,
  closing_carrying numeric(28,6) NOT NULL,
  unrecognized_loss numeric(28,6) NOT NULL DEFAULT 0,
  PRIMARY KEY (tenant_id, run_id, unit_id),
  FOREIGN KEY (tenant_id, run_id) REFERENCES consolidation_run (tenant_id, id)
);

CREATE OR REPLACE FUNCTION consolidation_investment_reject_change() RETURNS trigger
  LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'consolidation run data is immutable' USING ERRCODE = '55000';
END
$$;
DO $$
BEGIN
  EXECUTE 'CREATE TRIGGER consolidation_investment_immutable BEFORE UPDATE OR DELETE ON consolidation_investment FOR EACH ROW EXECUTE FUNCTION consolidation_investment_reject_change()';
  EXECUTE 'CREATE TRIGGER consolidation_investment_no_truncate BEFORE TRUNCATE ON consolidation_investment FOR EACH STATEMENT EXECUTE FUNCTION consolidation_investment_reject_change()';
END
$$;

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['consolidation_ic_layer', 'consolidation_investment'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format($p$CREATE POLICY %I_tenant_access ON %I AS PERMISSIVE
      USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
      WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid)$p$, t, t);
  END LOOP;
END
$$;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON consolidation_ic_layer TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON consolidation_investment TO passage_app';
  END IF;
END
$$;
