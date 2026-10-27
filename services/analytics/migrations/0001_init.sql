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

-- Daily reconciliation of a governed metric against its source (FR-46.13).
-- One row per legal entity, metric and day; re-running a day replaces the row.
CREATE TABLE IF NOT EXISTS metric_reconciliation (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  metric_id text NOT NULL CHECK (metric_id ~ '^[a-z][a-z0-9_]{1,62}$'),
  recon_date date NOT NULL,
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  semantic_value numeric(20,6) NOT NULL,
  source_value numeric(20,6) NOT NULL,
  delta numeric(20,6) NOT NULL,
  tolerance numeric(18,6) NOT NULL DEFAULT 0.005 CHECK (tolerance >= 0),
  status text NOT NULL CHECK (status IN ('OK', 'FAILED', 'BLOCKED')),
  consecutive_ok_days integer NOT NULL DEFAULT 0 CHECK (consecutive_ok_days >= 0),
  trusted boolean NOT NULL DEFAULT false,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id, metric_id, recon_date)
);
ALTER TABLE metric_reconciliation ENABLE ROW LEVEL SECURITY;
ALTER TABLE metric_reconciliation FORCE ROW LEVEL SECURITY;
CREATE POLICY metric_reconciliation_tenant_access ON metric_reconciliation AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY metric_reconciliation_legal_entity_scope ON metric_reconciliation AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
