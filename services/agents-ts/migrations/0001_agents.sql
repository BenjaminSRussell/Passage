-- Agent runtime: persisted runs, the per-action audit trail (AC-47.3), per-tenant
-- token budgets (AC-47.4) and the transactional outbox. Every table carries
-- tenant_id with ENABLE + FORCE row-level security and the tenant policy.
-- Migrations must run as the owner role: run as passage_app, the application
-- role would own the tables and could switch row-level security off.

DO $do$
BEGIN
  IF current_user = 'passage_app' THEN
    RAISE EXCEPTION 'agents migrations must run as the owner role, not passage_app';
  END IF;
END
$do$;

CREATE TABLE IF NOT EXISTS agent_run (
  tenant_id uuid NOT NULL,
  run_id text NOT NULL,
  agent_id text NOT NULL,
  agent_version integer NOT NULL CHECK (agent_version >= 1),
  invoker_user_id text NOT NULL,
  status text NOT NULL CHECK (status IN ('RUNNING', 'COMMITTED', 'REJECTED', 'FAILED', 'KILLED', 'FAILED_AFTER_COMMIT')),
  code text,
  reason text,
  tokens_in bigint NOT NULL CHECK (tokens_in >= 0),
  tokens_out bigint NOT NULL CHECK (tokens_out >= 0),
  estimated boolean NOT NULL,
  post_commit_error text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, run_id)
);
ALTER TABLE agent_run ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent_run FORCE ROW LEVEL SECURITY;
CREATE POLICY agent_run_tenant_access ON agent_run AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE OR REPLACE FUNCTION agent_run_guard() RETURNS trigger LANGUAGE plpgsql AS $guard$
BEGIN
  IF OLD.status <> 'RUNNING' THEN
    RAISE EXCEPTION 'agent_run % is finished and cannot change', OLD.run_id;
  END IF;
  IF NEW.tenant_id <> OLD.tenant_id OR NEW.run_id <> OLD.run_id OR NEW.agent_id <> OLD.agent_id
     OR NEW.agent_version <> OLD.agent_version OR NEW.invoker_user_id <> OLD.invoker_user_id THEN
    RAISE EXCEPTION 'agent_run % identity cannot change', OLD.run_id;
  END IF;
  RETURN NEW;
END
$guard$;
DROP TRIGGER IF EXISTS agent_run_guard ON agent_run;
CREATE TRIGGER agent_run_guard BEFORE UPDATE ON agent_run FOR EACH ROW EXECUTE FUNCTION agent_run_guard();

CREATE TABLE IF NOT EXISTS agent_action (
  tenant_id uuid NOT NULL,
  run_id text NOT NULL,
  seq integer NOT NULL CHECK (seq >= 1),
  tool_name text NOT NULL,
  input_json text NOT NULL,
  output_hash text NOT NULL CHECK (output_hash ~ '^[0-9a-f]{64}$'),
  output_json text NOT NULL,
  output_truncated boolean NOT NULL,
  approver_user_id text,
  decision text NOT NULL CHECK (decision IN ('APPROVED', 'REJECTED', 'AUTO', 'DENIED')),
  decided_at timestamptz NOT NULL,
  committed_entity_ref text,
  reason text,
  PRIMARY KEY (tenant_id, run_id, seq),
  FOREIGN KEY (tenant_id, run_id) REFERENCES agent_run (tenant_id, run_id),
  CHECK (committed_entity_ref IS NULL OR decision IN ('APPROVED', 'AUTO'))
);
ALTER TABLE agent_action ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent_action FORCE ROW LEVEL SECURITY;
CREATE POLICY agent_action_tenant_access ON agent_action AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- FR-47.11: one budget per tenant per month. Rows are created lazily from the
-- configured default; agent attribution lives on the holds, not the budget.
CREATE TABLE IF NOT EXISTS token_budget (
  tenant_id uuid NOT NULL,
  month text NOT NULL CHECK (month ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
  limit_in bigint NOT NULL CHECK (limit_in >= 0),
  limit_out bigint NOT NULL CHECK (limit_out >= 0),
  used_in bigint NOT NULL DEFAULT 0 CHECK (used_in >= 0),
  used_out bigint NOT NULL DEFAULT 0 CHECK (used_out >= 0),
  soft_pct integer NOT NULL DEFAULT 80 CHECK (soft_pct BETWEEN 1 AND 99),
  alert_state text NOT NULL DEFAULT 'OK' CHECK (alert_state IN ('OK', 'SOFT', 'HARD')),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, month)
);
ALTER TABLE token_budget ENABLE ROW LEVEL SECURITY;
ALTER TABLE token_budget FORCE ROW LEVEL SECURITY;
CREATE POLICY token_budget_tenant_access ON token_budget AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- A hold is the tokens a run may still spend on its model call. reserved = the sum of live
-- holds; a run settles it right after the call, and a hold whose worker died is swept and
-- charged in full once expires_at (database clock) passes.
CREATE TABLE IF NOT EXISTS budget_hold (
  tenant_id uuid NOT NULL,
  hold_id text NOT NULL,
  month text NOT NULL,
  agent_id text NOT NULL,
  run_id text NOT NULL,
  tokens_in bigint NOT NULL CHECK (tokens_in > 0),
  tokens_out bigint NOT NULL CHECK (tokens_out > 0),
  expires_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, hold_id),
  FOREIGN KEY (tenant_id, month) REFERENCES token_budget (tenant_id, month)
);
CREATE INDEX IF NOT EXISTS budget_hold_expiry ON budget_hold (tenant_id, expires_at);
ALTER TABLE budget_hold ENABLE ROW LEVEL SECURITY;
ALTER TABLE budget_hold FORCE ROW LEVEL SECURITY;
CREATE POLICY budget_hold_tenant_access ON budget_hold AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Transactional outbox, relayed by Debezium (kernel dbx.OutboxDDL), tenant-scoped like every table.
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

-- Application role privileges (skipped when the role does not exist: developer
-- databases without the role split). Action rows are never updated or deleted.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON agent_run TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON agent_action TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON token_budget TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON budget_hold TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON outbox TO passage_app';
  END IF;
END
$$;
