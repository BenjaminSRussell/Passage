-- Wave 41, D2b/D4c: project transactions and the POC estimate are booked in the general ledger.
-- A booking is the exact ledger entry of one transaction (kind TXN) or one project estimate period (kind WIP),
-- derived from stored data and the posting profile and written PENDING before the ledger is called. A retry resends
-- the stored payload under the same idempotency key until the ledger confirms it (POSTED). WIP bookings also carry the
-- cumulative position they stand for, so the next period books only the movement. The POSTED bookings are the project
-- GL subledger that GET /v1/projects/gl-reconciliation compares with the ledger's account balances.

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
  CHECK ((kind = 'WIP') = (revenue_to_date IS NOT NULL AND billed_to_date IS NOT NULL AND provision_to_date IS NOT NULL)),
  CHECK (status = 'POSTED' OR journal_entry_id IS NULL)
);
CREATE INDEX IF NOT EXISTS project_gl_booking_project_period ON project_gl_booking (tenant_id, project_id, kind, period);
CREATE INDEX IF NOT EXISTS project_gl_booking_account ON project_gl_booking (tenant_id, legal_entity_id, ledger_id, currency) WHERE status = 'POSTED';

ALTER TABLE project_gl_booking ENABLE ROW LEVEL SECURITY;
ALTER TABLE project_gl_booking FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS project_gl_booking_tenant_access ON project_gl_booking;
CREATE POLICY project_gl_booking_tenant_access ON project_gl_booking AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
DROP POLICY IF EXISTS project_gl_booking_legal_entity_scope ON project_gl_booking;
CREATE POLICY project_gl_booking_legal_entity_scope ON project_gl_booking AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON project_gl_booking TO passage_app';
  END IF;
END
$$;
