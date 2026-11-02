CREATE TABLE IF NOT EXISTS standard_cost_activation (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  costing_version_id uuid NOT NULL,
  reverses_activation_id uuid,
  ledger_id uuid NOT NULL,
  posting_date date NOT NULL,
  currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  item_count integer NOT NULL CHECK (item_count > 0),
  total numeric(19,4) NOT NULL,
  journal_entry_id text,
  inventory_account text NOT NULL DEFAULT '',
  cost_change_account text NOT NULL DEFAULT '',
  journal_lines jsonb NOT NULL DEFAULT '[]',
  new_standard_for_open_orders boolean NOT NULL DEFAULT false,
  lines jsonb NOT NULL,
  activated_by text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, costing_version_id) REFERENCES costing_version (tenant_id, id),
  FOREIGN KEY (tenant_id, reverses_activation_id) REFERENCES standard_cost_activation (tenant_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS standard_cost_activation_one_reversal
  ON standard_cost_activation (tenant_id, reverses_activation_id)
  WHERE reverses_activation_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS standard_cost_activation_version
  ON standard_cost_activation (tenant_id, costing_version_id, created_at);
ALTER TABLE standard_cost_activation ENABLE ROW LEVEL SECURITY;
ALTER TABLE standard_cost_activation FORCE ROW LEVEL SECURITY;
CREATE POLICY standard_cost_activation_tenant_access ON standard_cost_activation AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY standard_cost_activation_legal_entity_scope ON standard_cost_activation AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS standard_cost_activation_attempt (
  tenant_id uuid NOT NULL,
  attempt_key text NOT NULL,
  legal_entity_id uuid NOT NULL,
  costing_version_id uuid NOT NULL,
  payload jsonb NOT NULL,
  attempted_ambiguous boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, attempt_key),
  FOREIGN KEY (tenant_id, costing_version_id) REFERENCES costing_version (tenant_id, id)
);
ALTER TABLE standard_cost_activation_attempt ENABLE ROW LEVEL SECURITY;
ALTER TABLE standard_cost_activation_attempt FORCE ROW LEVEL SECURITY;
CREATE POLICY standard_cost_activation_attempt_tenant_access ON standard_cost_activation_attempt AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY standard_cost_activation_attempt_legal_entity_scope ON standard_cost_activation_attempt AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON standard_cost_activation TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON standard_cost_activation_attempt TO passage_app';
    EXECUTE 'GRANT UPDATE (attempted_ambiguous) ON standard_cost_activation_attempt TO passage_app';
    EXECUTE 'GRANT SELECT ON cost_calculation TO passage_app';
  END IF;
END
$$;
