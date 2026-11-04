-- Wave 41, D2b/D4c: project transactions and the POC estimate are booked in the general ledger.
-- A booking is the exact ledger entry of one transaction (kind TXN) or one project estimate period (kind WIP),
-- derived from stored data and the posting profile and written PENDING before the ledger is called. A retry resends
-- the stored payload under the same idempotency key until the ledger confirms it (POSTED). WIP bookings also carry the
-- cumulative position they stand for, so the next period books only the movement. The POSTED bookings are the project
-- GL subledger that GET /v1/projects/gl-reconciliation compares with the ledger's control account balances.

-- Accounts the GL postings need beyond the five of slice 2. offset_account is the credit of a transaction (payroll
-- allocation for hours, expense clearing for expenses, inventory for items: one profile row per category).
ALTER TABLE posting_profile
  ADD COLUMN IF NOT EXISTS offset_account text CHECK (offset_account IS NULL OR char_length(offset_account) BETWEEN 1 AND 40),
  ADD COLUMN IF NOT EXISTS cost_of_sales_account text CHECK (cost_of_sales_account IS NULL OR char_length(cost_of_sales_account) BETWEEN 1 AND 40),
  ADD COLUMN IF NOT EXISTS loss_provision_account text CHECK (loss_provision_account IS NULL OR char_length(loss_provision_account) BETWEEN 1 AND 40),
  ADD COLUMN IF NOT EXISTS loss_expense_account text CHECK (loss_expense_account IS NULL OR char_length(loss_expense_account) BETWEEN 1 AND 40);

-- The forecast model whose lines after a period are the estimate to complete (one per project).
ALTER TABLE project_forecast_model ADD COLUMN IF NOT EXISTS is_active boolean NOT NULL DEFAULT false;
CREATE UNIQUE INDEX IF NOT EXISTS project_forecast_model_one_active ON project_forecast_model (tenant_id, project_id) WHERE is_active;

-- Retry state of a transaction the ledger has not taken yet: due rows are selected by next_attempt_at, so rows that
-- keep failing back off and cannot starve newer ones; after the attempt limit the row is parked with its reason.
ALTER TABLE project_transaction
  ADD COLUMN IF NOT EXISTS gl_attempts integer NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS gl_last_error text,
  ADD COLUMN IF NOT EXISTS gl_next_attempt_at timestamptz,
  ADD COLUMN IF NOT EXISTS gl_parked_at timestamptz;
CREATE INDEX IF NOT EXISTS project_transaction_gl_due_idx ON project_transaction (tenant_id, txn_date, id)
  WHERE journal_id IS NULL AND cost_amount <> 0 AND gl_parked_at IS NULL;

-- The ledger a legal entity's project postings are pinned to by its first posting.
CREATE TABLE IF NOT EXISTS project_gl_ledger (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  ledger_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id)
);

CREATE TABLE IF NOT EXISTS project_gl_booking (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  project_id text NOT NULL,
  kind text NOT NULL CHECK (kind IN ('TXN', 'WIP')),
  txn_id uuid,
  period text CHECK (period IS NULL OR period ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
  ledger_id uuid NOT NULL,
  currency char(3) NOT NULL,
  posting_date date NOT NULL,
  idempotency_key text NOT NULL,
  lines jsonb NOT NULL,
  revenue_to_date numeric(20,6),
  billed_to_date numeric(20,6),
  provision_to_date numeric(20,6),
  cost_to_date numeric(20,6),
  status text NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'POSTED')),
  journal_entry_id uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  posted_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, project_id) REFERENCES project (tenant_id, id),
  FOREIGN KEY (tenant_id, txn_id) REFERENCES project_transaction (tenant_id, id),
  CHECK ((kind = 'TXN') = (txn_id IS NOT NULL)),
  CHECK ((kind = 'WIP') = (period IS NOT NULL)),
  CHECK ((kind = 'WIP') = (revenue_to_date IS NOT NULL AND billed_to_date IS NOT NULL AND provision_to_date IS NOT NULL AND cost_to_date IS NOT NULL)),
  CHECK (status = 'POSTED' OR journal_entry_id IS NULL)
);
CREATE INDEX IF NOT EXISTS project_gl_booking_project_period ON project_gl_booking (tenant_id, project_id, kind, period);
CREATE INDEX IF NOT EXISTS project_gl_booking_account ON project_gl_booking (tenant_id, legal_entity_id, ledger_id, currency) WHERE status = 'POSTED';

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['project_gl_booking', 'project_gl_ledger'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('DROP POLICY IF EXISTS %I_tenant_access ON %I', t, t);
    EXECUTE format('CREATE POLICY %I_tenant_access ON %I AS PERMISSIVE
      USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)
      WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)', t, t);
    EXECUTE format('DROP POLICY IF EXISTS %I_legal_entity_scope ON %I', t, t);
    EXECUTE format('CREATE POLICY %I_legal_entity_scope ON %I AS RESTRICTIVE
      USING (current_setting(''passage.legal_entity_scopes'', true) = ''*''
             OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))', t, t);
  END LOOP;
END $$;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON project_gl_booking TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON project_gl_ledger TO passage_app';
  END IF;
END
$$;
