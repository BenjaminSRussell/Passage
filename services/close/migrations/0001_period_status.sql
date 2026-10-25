CREATE TABLE IF NOT EXISTS period_status (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  ledger_id uuid NOT NULL,
  fiscal_period_id uuid NOT NULL,
  module text NOT NULL CHECK (module IN ('all', 'gl', 'ap', 'ar', 'inventory', 'assets', 'cash', 'production', 'projects', 'tax', 'bank', 'payroll')),
  status text NOT NULL CHECK (status IN ('open', 'on_hold', 'closed', 'permanently_closed')),
  allowed_group_id text,
  reopen_count integer NOT NULL DEFAULT 0,
  reopen_expires_at timestamptz,
  approvals jsonb NOT NULL DEFAULT '[]',
  changed_at timestamptz NOT NULL DEFAULT now(),
  changed_by text NOT NULL,
  PRIMARY KEY (tenant_id, ledger_id, fiscal_period_id, module)
);
ALTER TABLE period_status ENABLE ROW LEVEL SECURITY;
ALTER TABLE period_status FORCE ROW LEVEL SECURITY;
CREATE POLICY period_status_tenant_access ON period_status AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY period_status_legal_entity_scope ON period_status AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
