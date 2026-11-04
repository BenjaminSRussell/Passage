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
  amount numeric(20,6) NOT NULL CHECK (amount > 0),
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
  debit_account text NOT NULL,
  credit_account text NOT NULL,
  auto_reverse boolean NOT NULL,
  start_date date NOT NULL,
  status text NOT NULL CHECK (status IN ('scheduled', 'posted', 'reversed', 'parked', 'cancelled')),
  occurrences jsonb NOT NULL,
  entry_ids jsonb NOT NULL DEFAULT '{}',
  attempts int NOT NULL DEFAULT 0,
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  last_error text NOT NULL DEFAULT '',
  cancelled_by text NOT NULL DEFAULT '',
  reversals_pending boolean NOT NULL DEFAULT false,
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
CREATE TABLE IF NOT EXISTS regrouping_posted_rule (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  run_id uuid NOT NULL,
  ledger_id uuid NOT NULL,
  key_date date NOT NULL,
  rule_code varchar(20) NOT NULL,
  PRIMARY KEY (tenant_id, run_id, rule_code),
  UNIQUE (tenant_id, ledger_id, key_date, rule_code)
);
CREATE TABLE IF NOT EXISTS accrual_due (
  tenant_id uuid NOT NULL,
  run_id uuid NOT NULL,
  next_attempt_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, run_id)
);
CREATE INDEX IF NOT EXISTS accrual_due_next ON accrual_due (next_attempt_at);
DO $do$
BEGIN
  EXECUTE format($f$
    CREATE OR REPLACE FUNCTION accrual_run_track_due() RETURNS trigger
      LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$
    BEGIN
      IF NEW.status = 'scheduled'::text OR (NEW.status = 'cancelled'::text AND NEW.reversals_pending) THEN
        INSERT INTO accrual_due(tenant_id, run_id, next_attempt_at) VALUES (NEW.tenant_id, NEW.id, NEW.next_attempt_at)
        ON CONFLICT (tenant_id, run_id) DO UPDATE SET next_attempt_at = EXCLUDED.next_attempt_at;
      ELSE
        DELETE FROM accrual_due WHERE tenant_id = NEW.tenant_id AND run_id = NEW.id;
      END IF;
      RETURN NEW;
    END
    $body$;
    CREATE OR REPLACE FUNCTION close_accrual_due_tenants() RETURNS SETOF uuid
      LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$ SELECT DISTINCT tenant_id FROM accrual_due WHERE next_attempt_at <= pg_catalog.now() $body$;
  $f$, current_schema());
END
$do$;
REVOKE ALL ON FUNCTION accrual_run_track_due() FROM PUBLIC;
REVOKE ALL ON FUNCTION close_accrual_due_tenants() FROM PUBLIC;
DROP TRIGGER IF EXISTS accrual_run_due ON accrual_run;
CREATE TRIGGER accrual_run_due AFTER INSERT OR UPDATE ON accrual_run
  FOR EACH ROW EXECUTE FUNCTION accrual_run_track_due();
ALTER TABLE close_task ADD COLUMN IF NOT EXISTS reassigned_by text;
ALTER TABLE period_reopen_request ADD COLUMN IF NOT EXISTS rejected_by text;
ALTER TABLE period_reopen_request ADD COLUMN IF NOT EXISTS idempotency_key text NOT NULL DEFAULT '';
ALTER TABLE period_reopen_request ADD COLUMN IF NOT EXISTS request_hash text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS period_reopen_request_idempotency ON period_reopen_request (tenant_id, requested_by, idempotency_key) WHERE idempotency_key <> '';
ALTER TABLE close_run ADD COLUMN IF NOT EXISTS idempotency_key text NOT NULL DEFAULT '';
ALTER TABLE close_run ADD COLUMN IF NOT EXISTS request_hash text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS close_run_idempotency ON close_run (tenant_id, idempotency_key) WHERE idempotency_key <> '';
DO $$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['allocation_rule', 'allocation_run', 'accrual_scheme', 'accrual_run', 'regrouping_rule', 'regrouping_run', 'regrouping_posted_rule'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)', t || '_tenant_access', t);
    EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))', t || '_legal_entity_scope', t);
  END LOOP;
END
$$;
GRANT SELECT, INSERT, UPDATE ON allocation_rule, allocation_run, accrual_scheme, accrual_run, regrouping_rule, regrouping_run TO passage_app;
GRANT SELECT, INSERT, DELETE ON regrouping_posted_rule TO passage_app;
GRANT EXECUTE ON FUNCTION close_accrual_due_tenants() TO passage_app;
