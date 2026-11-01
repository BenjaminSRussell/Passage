-- Wave 40 AC4: stored consolidation runs (FR-40.10, FR-40.11). A run is the
-- header plus its balances, elimination entries and statement lines; all four
-- tables are append-only (triggers reject UPDATE, DELETE and TRUNCATE; the application
-- role is granted INSERT and SELECT only). A rerun of a group and period is a
-- new version, never an overwrite. Every table carries tenant_id and is
-- tenant-scoped by row-level security (dbx.RLSPolicy); the group is a column,
-- not a legal entity, because a group spans legal entities.

CREATE TABLE IF NOT EXISTS outbox (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL,
  aggregate_type text NOT NULL,
  aggregate_id text NOT NULL,
  type text NOT NULL,
  payload jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox FORCE ROW LEVEL SECURITY;
CREATE POLICY outbox_tenant_access ON outbox AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS idempotency_keys (
  tenant_id uuid NOT NULL,
  key text NOT NULL,
  request_hash text NOT NULL,
  response_status int NOT NULL,
  response_body jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, key)
);
ALTER TABLE idempotency_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE idempotency_keys FORCE ROW LEVEL SECURITY;
CREATE POLICY idempotency_keys_tenant_access ON idempotency_keys AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS consolidation_run (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  group_id text NOT NULL CHECK (length(group_id) BETWEEN 1 AND 64),
  period text NOT NULL CHECK (period ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
  currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  version integer NOT NULL CHECK (version >= 1),
  status text NOT NULL CHECK (status IN ('completed')),
  inputs_hash text NOT NULL,
  inputs jsonb NOT NULL,
  unit_count integer NOT NULL CHECK (unit_count >= 1),
  elimination_count integer NOT NULL CHECK (elimination_count >= 0),
  cta_amount numeric(28,6) NOT NULL,
  created_by text NOT NULL CHECK (length(created_by) >= 1),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, group_id, period, currency, version)
);
CREATE INDEX IF NOT EXISTS consolidation_run_group_period
  ON consolidation_run (tenant_id, group_id, period, version DESC);

CREATE TABLE IF NOT EXISTS consolidation_balance (
  tenant_id uuid NOT NULL,
  run_id uuid NOT NULL,
  seq integer NOT NULL,
  unit_id text NOT NULL,
  account text NOT NULL,
  amount numeric(28,6) NOT NULL,
  PRIMARY KEY (tenant_id, run_id, seq),
  FOREIGN KEY (tenant_id, run_id) REFERENCES consolidation_run (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS consolidation_entry (
  tenant_id uuid NOT NULL,
  run_id uuid NOT NULL,
  seq integer NOT NULL,
  unit_id text NOT NULL,
  rule_code text NOT NULL,
  account text NOT NULL,
  debit numeric(28,6) NOT NULL CHECK (debit >= 0),
  credit numeric(28,6) NOT NULL CHECK (credit >= 0),
  PRIMARY KEY (tenant_id, run_id, seq),
  FOREIGN KEY (tenant_id, run_id) REFERENCES consolidation_run (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS consolidation_statement_line (
  tenant_id uuid NOT NULL,
  run_id uuid NOT NULL,
  seq integer NOT NULL,
  statement text NOT NULL,
  line_code text NOT NULL,
  amount numeric(28,6) NOT NULL,
  PRIMARY KEY (tenant_id, run_id, seq),
  FOREIGN KEY (tenant_id, run_id) REFERENCES consolidation_run (tenant_id, id)
);

CREATE OR REPLACE FUNCTION consolidation_reject_change() RETURNS trigger
  LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'consolidation run data is immutable: rerun to create a new version'
    USING ERRCODE = '55000';
END
$$;

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['consolidation_run', 'consolidation_balance', 'consolidation_entry', 'consolidation_statement_line'] LOOP
    EXECUTE format('CREATE TRIGGER %I_immutable BEFORE UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION consolidation_reject_change()', t, t);
    EXECUTE format('CREATE TRIGGER %I_no_truncate BEFORE TRUNCATE ON %I FOR EACH STATEMENT EXECUTE FUNCTION consolidation_reject_change()', t, t);
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format($p$CREATE POLICY %I_tenant_access ON %I AS PERMISSIVE
      USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
      WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid)$p$, t, t);
  END LOOP;
END
$$;
