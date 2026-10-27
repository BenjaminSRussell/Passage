-- Financial reporting: definitions, reporting balance read model, statement runs and cells.
-- Every table carries tenant_id first and enables tenant row-level security.

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

-- Report definitions. A code has at most one draft (version 0, editable, revision-checked)
-- and any number of immutable published versions (1..n).
CREATE TABLE IF NOT EXISTS report_definition (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z][A-Z0-9-]{1,29}$'),
  version integer NOT NULL DEFAULT 0 CHECK (version >= 0),
  status text NOT NULL CHECK (status IN ('draft', 'published', 'retired')),
  revision integer NOT NULL DEFAULT 1,
  name text NOT NULL,
  kind text NOT NULL CHECK (kind IN ('trial_balance', 'balance_sheet', 'income_statement', 'cash_flow', 'custom')),
  body jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  published_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code, version)
);
CREATE UNIQUE INDEX IF NOT EXISTS report_definition_one_draft ON report_definition (tenant_id, code) WHERE status = 'draft';
ALTER TABLE report_definition ENABLE ROW LEVEL SECURITY;
ALTER TABLE report_definition FORCE ROW LEVEL SECURITY;
CREATE POLICY report_definition_tenant_access ON report_definition AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Read model: one row per ledger x period x account x dimension combination x currency type.
-- net = debit - credit; closing_net = the part of net posted by closing entries (excluded from P&L).
CREATE TABLE IF NOT EXISTS reporting_balance (
  tenant_id uuid NOT NULL,
  ledger_id uuid NOT NULL,
  period_year integer NOT NULL,
  period_no integer NOT NULL,
  quarter integer NOT NULL CHECK (quarter BETWEEN 1 AND 4),
  adjustment boolean NOT NULL DEFAULT false,
  account text NOT NULL,
  combination text NOT NULL,
  dims jsonb NOT NULL DEFAULT '{}',
  currency_type text NOT NULL CHECK (currency_type IN ('acct', 'rpt', 'txn')),
  net numeric(20,6) NOT NULL DEFAULT 0,
  closing_net numeric(20,6) NOT NULL DEFAULT 0,
  PRIMARY KEY (tenant_id, ledger_id, period_year, period_no, account, combination, currency_type)
);
CREATE INDEX IF NOT EXISTS reporting_balance_ledger_account ON reporting_balance (tenant_id, ledger_id, currency_type, account);
ALTER TABLE reporting_balance ENABLE ROW LEVEL SECURITY;
ALTER TABLE reporting_balance FORCE ROW LEVEL SECURITY;
CREATE POLICY reporting_balance_tenant_access ON reporting_balance AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Highest posting sequence the read model reflects, per ledger.
CREATE TABLE IF NOT EXISTS reporting_hwm (
  tenant_id uuid NOT NULL,
  ledger_id uuid NOT NULL,
  posting_sequence bigint NOT NULL CHECK (posting_sequence >= 0),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, ledger_id)
);
ALTER TABLE reporting_hwm ENABLE ROW LEVEL SECURITY;
ALTER TABLE reporting_hwm FORCE ROW LEVEL SECURITY;
CREATE POLICY reporting_hwm_tenant_access ON reporting_hwm AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS statement_run (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  definition_code text NOT NULL,
  definition_version integer NOT NULL CHECK (definition_version >= 1),
  ledger_id uuid NOT NULL,
  currency_type text NOT NULL CHECK (currency_type IN ('acct', 'rpt', 'txn')),
  period_year integer NOT NULL,
  period_no integer NOT NULL,
  include_adjustment boolean NOT NULL DEFAULT false,
  status text NOT NULL CHECK (status IN ('queued', 'running', 'completed', 'failed')),
  hwm jsonb NOT NULL,
  result_hash bytea,
  cell_count integer NOT NULL DEFAULT 0,
  requested_by text NOT NULL DEFAULT '',
  request_hash text NOT NULL,
  idempotency_key text,
  started_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  duration_ms integer,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key)
);
ALTER TABLE statement_run ENABLE ROW LEVEL SECURITY;
ALTER TABLE statement_run FORCE ROW LEVEL SECURITY;
CREATE POLICY statement_run_tenant_access ON statement_run AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS statement_cell (
  tenant_id uuid NOT NULL,
  run_id uuid NOT NULL,
  row_code text NOT NULL,
  column_code text NOT NULL,
  value numeric(23,6) NOT NULL DEFAULT 0,
  blank boolean NOT NULL DEFAULT false,
  computed boolean NOT NULL DEFAULT false,
  note text NOT NULL DEFAULT '',
  balance_keys jsonb NOT NULL DEFAULT '[]',
  line_count integer NOT NULL DEFAULT 0,
  position integer NOT NULL DEFAULT 0,
  PRIMARY KEY (tenant_id, run_id, row_code, column_code),
  FOREIGN KEY (tenant_id, run_id) REFERENCES statement_run (tenant_id, id)
);
ALTER TABLE statement_cell ENABLE ROW LEVEL SECURITY;
ALTER TABLE statement_cell FORCE ROW LEVEL SECURITY;
CREATE POLICY statement_cell_tenant_access ON statement_cell AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Idempotency-Key replay store for commands without their own key column
-- (definition import); same shape as dbx.IdempotencyDDL.
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

CREATE INDEX IF NOT EXISTS statement_run_started ON statement_run (tenant_id, started_at DESC, id);
