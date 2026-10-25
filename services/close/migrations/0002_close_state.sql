CREATE TABLE IF NOT EXISTS outbox (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL,
  aggregate_type text NOT NULL,
  aggregate_id text NOT NULL,
  type text NOT NULL,
  partition_key text,
  payload jsonb NOT NULL,
  headers jsonb NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS idempotency_keys (
  tenant_id uuid NOT NULL,
  key text NOT NULL,
  request_hash text NOT NULL,
  response_status int,
  response_body jsonb,
  locked_until timestamptz,
  expires_at timestamptz NOT NULL DEFAULT now() + interval '24 hours',
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, key)
);
CREATE TABLE IF NOT EXISTS period_status_change (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  ledger_id uuid NOT NULL,
  fiscal_period_id uuid NOT NULL,
  module text NOT NULL,
  from_status text NOT NULL,
  to_status text NOT NULL,
  actor text NOT NULL,
  reason text,
  approvals jsonb NOT NULL DEFAULT '[]',
  changed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS period_status_change_period ON period_status_change (tenant_id, ledger_id, fiscal_period_id, changed_at);
CREATE TABLE IF NOT EXISTS period_reopen_request (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  ledger_id uuid NOT NULL,
  fiscal_period_id uuid NOT NULL,
  module text NOT NULL,
  reason text NOT NULL,
  requested_by text NOT NULL,
  approvals jsonb NOT NULL DEFAULT '[]',
  required_approvals int NOT NULL CHECK (required_approvals BETWEEN 2 AND 3),
  status text NOT NULL CHECK (status IN ('pending', 'approved', 'rejected', 'cancelled')),
  expires_days int NOT NULL CHECK (expires_days BETWEEN 1 AND 30),
  created_at timestamptz NOT NULL DEFAULT now(),
  decided_at timestamptz,
  PRIMARY KEY (tenant_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS period_reopen_request_one_pending ON period_reopen_request (tenant_id, ledger_id, fiscal_period_id, module) WHERE status = 'pending';
CREATE TABLE IF NOT EXISTS close_template (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  name varchar(60) NOT NULL,
  version int NOT NULL DEFAULT 1,
  updated_at timestamptz NOT NULL DEFAULT now(),
  updated_by text NOT NULL,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, name)
);
CREATE TABLE IF NOT EXISTS close_template_task (
  tenant_id uuid NOT NULL,
  template_id uuid NOT NULL,
  task_key text NOT NULL,
  name text NOT NULL,
  description text NOT NULL DEFAULT '',
  module text NOT NULL,
  assignee_role text NOT NULL,
  due_offset_days int NOT NULL CHECK (due_offset_days BETWEEN -10 AND 30),
  depends_on text[] NOT NULL DEFAULT '{}',
  evidence_required boolean NOT NULL DEFAULT false,
  automation jsonb,
  manual_override_allowed boolean NOT NULL DEFAULT false,
  position int NOT NULL,
  PRIMARY KEY (tenant_id, template_id, task_key),
  FOREIGN KEY (tenant_id, template_id) REFERENCES close_template (tenant_id, id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS close_run (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  template_id uuid NOT NULL,
  template_version int NOT NULL,
  fiscal_period_id uuid NOT NULL,
  period_end date NOT NULL,
  legal_entity_ids uuid[] NOT NULL,
  status text NOT NULL CHECK (status IN ('in_progress', 'completed', 'cancelled')),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  PRIMARY KEY (tenant_id, id)
);
CREATE TABLE IF NOT EXISTS close_task (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  run_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  task_key text NOT NULL,
  module text NOT NULL,
  name text NOT NULL,
  description text NOT NULL DEFAULT '',
  assignee_role text NOT NULL,
  assignee_user_id text,
  due_at timestamptz NOT NULL,
  depends_on uuid[] NOT NULL DEFAULT '{}',
  status text NOT NULL CHECK (status IN ('not_started', 'in_progress', 'completed', 'skipped')),
  evidence_required boolean NOT NULL DEFAULT false,
  evidence_uri text,
  automation jsonb,
  manual_override_allowed boolean NOT NULL DEFAULT false,
  skip_reason text,
  started_at timestamptz,
  completed_at timestamptz,
  completed_by text,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, run_id) REFERENCES close_run (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS close_task_run_status ON close_task (tenant_id, run_id, status);
CREATE INDEX IF NOT EXISTS close_task_assignee_due ON close_task (tenant_id, assignee_user_id, due_at);
DO $$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['outbox', 'idempotency_keys', 'period_status_change', 'period_reopen_request', 'close_template', 'close_template_task', 'close_run', 'close_task'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)', t || '_tenant_access', t);
  END LOOP;
  FOREACH t IN ARRAY ARRAY['period_status_change', 'period_reopen_request', 'close_task'] LOOP
    EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))', t || '_legal_entity_scope', t);
  END LOOP;
END
$$;
CREATE TABLE IF NOT EXISTS reopen_expiry (
  tenant_id uuid NOT NULL,
  ledger_id uuid NOT NULL,
  fiscal_period_id uuid NOT NULL,
  module text NOT NULL,
  reopen_expires_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, ledger_id, fiscal_period_id, module)
);
CREATE INDEX IF NOT EXISTS reopen_expiry_due ON reopen_expiry (reopen_expires_at);
CREATE OR REPLACE FUNCTION period_status_track_reopen_expiry() RETURNS trigger
  LANGUAGE plpgsql SECURITY DEFINER SET search_path FROM CURRENT
  AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    DELETE FROM reopen_expiry WHERE tenant_id = OLD.tenant_id AND ledger_id = OLD.ledger_id AND fiscal_period_id = OLD.fiscal_period_id AND module = OLD.module;
    RETURN OLD;
  END IF;
  IF NEW.reopen_expires_at IS NULL OR NEW.status NOT IN ('open', 'on_hold') THEN
    DELETE FROM reopen_expiry WHERE tenant_id = NEW.tenant_id AND ledger_id = NEW.ledger_id AND fiscal_period_id = NEW.fiscal_period_id AND module = NEW.module;
  ELSE
    INSERT INTO reopen_expiry(tenant_id, ledger_id, fiscal_period_id, module, reopen_expires_at)
    VALUES (NEW.tenant_id, NEW.ledger_id, NEW.fiscal_period_id, NEW.module, NEW.reopen_expires_at)
    ON CONFLICT (tenant_id, ledger_id, fiscal_period_id, module) DO UPDATE SET reopen_expires_at = EXCLUDED.reopen_expires_at;
  END IF;
  RETURN NEW;
END
$$;
REVOKE ALL ON FUNCTION period_status_track_reopen_expiry() FROM PUBLIC;
DROP TRIGGER IF EXISTS period_status_reopen_expiry ON period_status;
CREATE TRIGGER period_status_reopen_expiry AFTER INSERT OR UPDATE OR DELETE ON period_status
  FOR EACH ROW EXECUTE FUNCTION period_status_track_reopen_expiry();
INSERT INTO reopen_expiry(tenant_id, ledger_id, fiscal_period_id, module, reopen_expires_at)
  SELECT tenant_id, ledger_id, fiscal_period_id, module, reopen_expires_at FROM period_status
  WHERE reopen_expires_at IS NOT NULL AND status IN ('open', 'on_hold')
ON CONFLICT DO NOTHING;
CREATE OR REPLACE FUNCTION close_reopen_due_tenants() RETURNS SETOF uuid
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path FROM CURRENT
  AS $$ SELECT DISTINCT tenant_id FROM reopen_expiry WHERE reopen_expires_at <= now() $$;
REVOKE ALL ON FUNCTION close_reopen_due_tenants() FROM PUBLIC;
DO $$
BEGIN
  EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
END
$$;
GRANT SELECT, INSERT, UPDATE, DELETE ON
  period_status,
  outbox,
  idempotency_keys,
  period_status_change,
  period_reopen_request,
  close_template,
  close_template_task,
  close_run,
  close_task
TO passage_app;
GRANT EXECUTE ON FUNCTION close_reopen_due_tenants() TO passage_app;
