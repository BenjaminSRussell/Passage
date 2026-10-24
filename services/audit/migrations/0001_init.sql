CREATE TABLE IF NOT EXISTS audit_entry (
  tenant_id uuid NOT NULL,
  seq bigserial NOT NULL,
  id uuid NOT NULL,
  entity text NOT NULL,
  record_id text NOT NULL,
  field text NOT NULL,
  old_value text NOT NULL DEFAULT '',
  new_value text NOT NULL DEFAULT '',
  user_id text NOT NULL,
  trace_id text NOT NULL,
  occurred_at timestamptz NOT NULL,
  prev_hash char(64) NOT NULL,
  hash char(64) NOT NULL,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, seq),
  UNIQUE (tenant_id, hash)
);
ALTER TABLE audit_entry ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_entry FORCE ROW LEVEL SECURITY;
CREATE POLICY audit_entry_tenant_access ON audit_entry AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE INDEX IF NOT EXISTS audit_entry_record ON audit_entry (tenant_id, entity, record_id, seq);

CREATE FUNCTION audit_entry_immutable() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'audit_entry is append-only';
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER audit_entry_no_mutation
  BEFORE UPDATE OR DELETE ON audit_entry
  FOR EACH ROW EXECUTE PROCEDURE audit_entry_immutable();

CREATE TABLE IF NOT EXISTS alert_rule (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  entity text NOT NULL,
  field text NOT NULL DEFAULT '',
  kind text NOT NULL CHECK (kind IN ('change', 'due_date')),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id)
);
ALTER TABLE alert_rule ENABLE ROW LEVEL SECURITY;
ALTER TABLE alert_rule FORCE ROW LEVEL SECURITY;
CREATE POLICY alert_rule_tenant_access ON alert_rule AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS alert_inbox (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  rule_id uuid NOT NULL,
  entity text NOT NULL,
  record_id text NOT NULL,
  field text NOT NULL,
  message text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id)
);
ALTER TABLE alert_inbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE alert_inbox FORCE ROW LEVEL SECURITY;
CREATE POLICY alert_inbox_tenant_access ON alert_inbox AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

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
