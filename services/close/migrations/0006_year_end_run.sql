CREATE TABLE IF NOT EXISTS year_end_run (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  ledger_id uuid NOT NULL,
  fiscal_year text NOT NULL CHECK (fiscal_year ~ '^[0-9]{4}$'),
  sequence int NOT NULL CHECK (sequence >= 1),
  supersedes_run_id uuid,
  request_hash text NOT NULL,
  status text NOT NULL CHECK (status IN ('pending', 'completed')),
  currency text NOT NULL,
  closing_date date NOT NULL,
  opening_date date NOT NULL,
  retained_earnings_account text NOT NULL,
  retain_dimensions boolean NOT NULL DEFAULT false,
  proposals jsonb,
  journals jsonb NOT NULL DEFAULT '{}',
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, ledger_id, fiscal_year, sequence)
);
ALTER TABLE year_end_run ENABLE ROW LEVEL SECURITY;
ALTER TABLE year_end_run FORCE ROW LEVEL SECURITY;
CREATE POLICY year_end_run_tenant_access ON year_end_run AS PERMISSIVE USING (tenant_id = current_setting('passage.tenant_id', true)::uuid) WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY year_end_run_legal_entity_scope ON year_end_run AS RESTRICTIVE USING (current_setting('passage.legal_entity_scopes', true) = '*' OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
GRANT SELECT, INSERT, UPDATE ON year_end_run TO passage_app;
