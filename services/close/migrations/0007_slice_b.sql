CREATE TABLE IF NOT EXISTS allocation_rule (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  ledger_id uuid NOT NULL,
  name varchar(60) NOT NULL,
  method text NOT NULL CHECK (method IN ('fixed_percentage', 'fixed_weight', 'equally', 'basis')),
  source_filter jsonb NOT NULL,
  destinations jsonb NOT NULL,
  offset_account text NOT NULL,
  offset_dimensions text NOT NULL DEFAULT '',
  iterative_group text NOT NULL DEFAULT '',
  auto_reverse_next_period boolean NOT NULL DEFAULT false,
  allocate_from_zero boolean NOT NULL DEFAULT false,
  updated_by text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, ledger_id, name)
);
CREATE TABLE IF NOT EXISTS allocation_run (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  ledger_id uuid NOT NULL,
  fiscal_period_id uuid NOT NULL,
  rule_id uuid NOT NULL,
  posting_date date NOT NULL,
  currency text NOT NULL,
  source_amount numeric(20,6) NOT NULL,
  mode text NOT NULL CHECK (mode IN ('simulate', 'post')),
  status text NOT NULL CHECK (status IN ('proposed', 'posted', 'reversed', 'cancelled')),
  proposal jsonb NOT NULL,
  snapshot jsonb NOT NULL DEFAULT '{}',
  entry_id text NOT NULL DEFAULT '',
  reversal_entry_id text NOT NULL DEFAULT '',
  idempotency_key text NOT NULL,
  request_hash text NOT NULL,
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key)
);
CREATE TABLE IF NOT EXISTS accrual_scheme (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  ledger_id uuid NOT NULL,
  code varchar(20) NOT NULL,
  debit_account text NOT NULL,
  credit_account text NOT NULL,
  frequency text NOT NULL CHECK (frequency IN ('monthly', 'quarterly', 'custom')),
  occurrences int NOT NULL CHECK (occurrences BETWEEN 1 AND 120),
  spread text NOT NULL CHECK (spread IN ('equal', 'by_days', 'custom')),
  custom jsonb NOT NULL DEFAULT '[]',
  auto_reverse boolean NOT NULL DEFAULT true,
  updated_by text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, ledger_id, code)
);
CREATE TABLE IF NOT EXISTS accrual_run (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  ledger_id uuid NOT NULL,
  scheme_code varchar(20) NOT NULL,
  amount numeric(20,6) NOT NULL,
  currency text NOT NULL,
  start_date date NOT NULL,
  status text NOT NULL CHECK (status IN ('scheduled', 'posted', 'reversed')),
  occurrences jsonb NOT NULL,
  entry_ids jsonb NOT NULL DEFAULT '{}',
  idempotency_key text NOT NULL,
  request_hash text NOT NULL,
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key)
);
CREATE TABLE IF NOT EXISTS regrouping_rule (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  ledger_id uuid NOT NULL,
  code varchar(20) NOT NULL,
  sort_method text NOT NULL CHECK (sort_method IN ('remaining_term', 'debit_credit_balance', 'account_balance_sign')),
  source_account text NOT NULL DEFAULT '',
  buckets jsonb NOT NULL DEFAULT '[]',
  adjustment_account text NOT NULL DEFAULT '',
  target_account text NOT NULL DEFAULT '',
  normal_balance text NOT NULL DEFAULT '',
  sequence int NOT NULL DEFAULT 1,
  active boolean NOT NULL DEFAULT true,
  updated_by text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, ledger_id, code)
);
CREATE TABLE IF NOT EXISTS regrouping_run (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  ledger_id uuid NOT NULL,
  key_date date NOT NULL,
  rule_codes text[] NOT NULL,
  mode text NOT NULL CHECK (mode IN ('simulate', 'post')),
  status text NOT NULL CHECK (status IN ('proposed', 'posted', 'reversed', 'cancelled')),
  proposal jsonb NOT NULL,
  snapshot jsonb NOT NULL DEFAULT '{}',
  entry_id text NOT NULL DEFAULT '',
  reversal_entry_id text NOT NULL DEFAULT '',
  idempotency_key text NOT NULL,
  request_hash text NOT NULL,
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key)
);
CREATE UNIQUE INDEX IF NOT EXISTS regrouping_run_posted ON regrouping_run (tenant_id, ledger_id, key_date, rule_codes) WHERE status = 'posted';
ALTER TABLE close_run ADD COLUMN IF NOT EXISTS idempotency_key text NOT NULL DEFAULT '';
ALTER TABLE close_run ADD COLUMN IF NOT EXISTS request_hash text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS close_run_idempotency ON close_run (tenant_id, idempotency_key) WHERE idempotency_key <> '';
DO $$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['allocation_rule', 'allocation_run', 'accrual_scheme', 'accrual_run', 'regrouping_rule', 'regrouping_run'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)', t || '_tenant_access', t);
    EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))', t || '_legal_entity_scope', t);
  END LOOP;
END
$$;
GRANT SELECT, INSERT, UPDATE ON allocation_rule, allocation_run, accrual_scheme, accrual_run, regrouping_rule, regrouping_run TO passage_app;
