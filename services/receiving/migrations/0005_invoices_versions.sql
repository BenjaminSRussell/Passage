ALTER TABLE receiving_receipt
  ADD COLUMN IF NOT EXISTS row_version int NOT NULL DEFAULT 1,
  ADD COLUMN IF NOT EXISTS correction_seq int NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS receiving_vendor_invoice (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  vendor_id uuid NOT NULL,
  invoice_number text NOT NULL,
  invoice_date date NOT NULL,
  posting_date date NOT NULL,
  currency char(3) NOT NULL,
  ledger_id uuid NOT NULL,
  fiscal_period_id uuid NOT NULL,
  ledger_entry_id text NOT NULL,
  total numeric(20,6) NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id)
);

CREATE UNIQUE INDEX IF NOT EXISTS receiving_vendor_invoice_number_uq ON receiving_vendor_invoice (tenant_id, vendor_id, lower(invoice_number));

CREATE TABLE IF NOT EXISTS receiving_vendor_invoice_line (
  tenant_id uuid NOT NULL,
  invoice_id uuid NOT NULL,
  line_no int NOT NULL,
  legal_entity_id uuid NOT NULL,
  receipt_id uuid NOT NULL,
  receipt_line_no int NOT NULL,
  qty numeric(20,6) NOT NULL CHECK (qty > 0),
  unit_price numeric(20,6) NOT NULL CHECK (unit_price >= 0),
  amount numeric(20,6) NOT NULL,
  standard_cost boolean NOT NULL DEFAULT false,
  PRIMARY KEY (tenant_id, invoice_id, line_no),
  FOREIGN KEY (tenant_id, invoice_id) REFERENCES receiving_vendor_invoice (tenant_id, id),
  FOREIGN KEY (tenant_id, receipt_id, receipt_line_no) REFERENCES receiving_receipt_line (tenant_id, receipt_id, line_no)
);

DO $$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['receiving_vendor_invoice', 'receiving_vendor_invoice_line'] LOOP
    IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE schemaname = current_schema() AND tablename = t AND policyname = t || '_tenant_access') THEN
      EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
      EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
      EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)', t || '_tenant_access', t);
      EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))', t || '_legal_entity_scope', t);
    END IF;
  END LOOP;
END
$$;

CREATE TABLE IF NOT EXISTS receiving_pending_posting (
  tenant_id uuid NOT NULL,
  ledger_key text NOT NULL,
  subject text NOT NULL,
  request_hash text NOT NULL,
  journal jsonb NOT NULL,
  outcome text CHECK (outcome IN ('POSTED', 'REJECTED')),
  created_at timestamptz NOT NULL DEFAULT now(),
  done_at timestamptz,
  PRIMARY KEY (tenant_id, ledger_key)
);

CREATE INDEX IF NOT EXISTS receiving_pending_posting_subject_ix ON receiving_pending_posting (tenant_id, subject) WHERE done_at IS NULL;

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE schemaname = current_schema() AND tablename = 'receiving_pending_posting' AND policyname = 'receiving_pending_posting_tenant_access') THEN
    ALTER TABLE receiving_pending_posting ENABLE ROW LEVEL SECURITY;
    ALTER TABLE receiving_pending_posting FORCE ROW LEVEL SECURITY;
    CREATE POLICY receiving_pending_posting_tenant_access ON receiving_pending_posting AS PERMISSIVE USING (tenant_id = current_setting('passage.tenant_id', true)::uuid) WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
  END IF;
END
$$;

GRANT SELECT, INSERT ON receiving_vendor_invoice, receiving_vendor_invoice_line TO passage_app;
GRANT SELECT, INSERT, UPDATE ON receiving_pending_posting TO passage_app;
