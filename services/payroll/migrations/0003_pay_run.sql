CREATE TABLE IF NOT EXISTS pay_run (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  pay_date date NOT NULL,
  period_start date NOT NULL,
  period_end date NOT NULL,
  status text NOT NULL CHECK (status IN ('CALCULATED', 'REVIEWED', 'FINALIZED', 'POSTED')),
  calculated_by text NOT NULL,
  finalized_by text,
  idempotency_key text NOT NULL,
  request_hash text NOT NULL,
  ledger_id uuid,
  journal_entry_id uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  CHECK (period_end >= period_start)
);

CREATE TABLE IF NOT EXISTS pay_statement (
  tenant_id uuid NOT NULL,
  run_id uuid NOT NULL,
  worker_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  rule_set text NOT NULL,
  hash text NOT NULL,
  gross numeric(18, 2) NOT NULL,
  net numeric(18, 2) NOT NULL,
  employer_cost numeric(18, 2) NOT NULL,
  lines jsonb NOT NULL,
  PRIMARY KEY (tenant_id, run_id, worker_id),
  FOREIGN KEY (tenant_id, run_id) REFERENCES pay_run (tenant_id, id)
);

ALTER TABLE pay_run ENABLE ROW LEVEL SECURITY;
ALTER TABLE pay_run FORCE ROW LEVEL SECURITY;
CREATE POLICY pay_run_tenant_access ON pay_run AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY pay_run_legal_entity_scope ON pay_run AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE pay_statement ENABLE ROW LEVEL SECURITY;
ALTER TABLE pay_statement FORCE ROW LEVEL SECURITY;
CREATE POLICY pay_statement_tenant_access ON pay_statement AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY pay_statement_legal_entity_scope ON pay_statement AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON pay_run TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON pay_statement TO passage_app';
  END IF;
END
$$;
