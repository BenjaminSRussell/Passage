-- Wave 16 slice 7: daily inventory-to-ledger reconciliation (FR-16.12).
-- Additive only: one result row per legal entity, inventory account, currency and
-- run date (the PRD run id); a re-run on the same day overwrites the row.

CREATE TABLE IF NOT EXISTS reconciliation_run (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  id uuid NOT NULL,
  account text NOT NULL,
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  run_date date NOT NULL,
  inventory_value numeric(20,6) NOT NULL,
  ledger_balance numeric(20,6) NOT NULL,
  diff numeric(20,6) NOT NULL,
  status text NOT NULL CHECK (status IN ('MATCHED', 'DIFFERENCE')),
  ledger_source text NOT NULL CHECK (ledger_source IN ('GL_JOURNALS', 'LEDGER_OVERRIDE')),
  negative_rows integer NOT NULL DEFAULT 0 CHECK (negative_rows >= 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, account, currency, run_date),
  CHECK ((status = 'MATCHED') = (diff = 0))
);
CREATE INDEX IF NOT EXISTS reconciliation_run_date_idx ON reconciliation_run (tenant_id, run_date DESC, created_at DESC);
ALTER TABLE reconciliation_run ENABLE ROW LEVEL SECURITY;
ALTER TABLE reconciliation_run FORCE ROW LEVEL SECURITY;
CREATE POLICY reconciliation_run_tenant_access ON reconciliation_run AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY reconciliation_run_legal_entity_scope ON reconciliation_run AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON reconciliation_run TO passage_app';
  END IF;
END
$$;
