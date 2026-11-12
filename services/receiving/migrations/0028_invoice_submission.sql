-- Supplier portal (#511): an invoice a vendor submits against received lines of its own purchase orders. It is not a posted invoice:
-- accounts payable reviews it and keys the invoice (or rejects the submission). Until then its quantities are reserved, so the same
-- received line cannot be submitted twice. Additive only.
CREATE TABLE IF NOT EXISTS receiving_invoice_submission (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  vendor_id uuid NOT NULL,
  invoice_number text NOT NULL CHECK (char_length(invoice_number) BETWEEN 1 AND 64),
  invoice_date date NOT NULL,
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  total numeric(20,6) NOT NULL CHECK (total >= 0),
  note text NOT NULL DEFAULT '' CHECK (char_length(note) <= 1000),
  status text NOT NULL CHECK (status IN ('SUBMITTED', 'WITHDRAWN', 'REJECTED', 'POSTED')),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  decided_by text,
  decided_at timestamptz,
  decision_note text NOT NULL DEFAULT '' CHECK (char_length(decision_note) <= 1000),
  invoice_id uuid,
  PRIMARY KEY (tenant_id, id),
  CHECK ((status IN ('REJECTED', 'POSTED')) = (decided_at IS NOT NULL)),
  CHECK ((status = 'POSTED') = (invoice_id IS NOT NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS receiving_invoice_submission_number_ux ON receiving_invoice_submission (tenant_id, vendor_id, lower(invoice_number)) WHERE status IN ('SUBMITTED', 'POSTED');
CREATE INDEX IF NOT EXISTS receiving_invoice_submission_vendor_ix ON receiving_invoice_submission (tenant_id, vendor_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS receiving_invoice_submission_status_ix ON receiving_invoice_submission (tenant_id, status, created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS receiving_invoice_submission_line (
  tenant_id uuid NOT NULL,
  submission_id uuid NOT NULL,
  line_no int NOT NULL CHECK (line_no > 0),
  legal_entity_id uuid NOT NULL,
  receipt_id uuid NOT NULL,
  receipt_line_no int NOT NULL,
  qty numeric(20,6) NOT NULL CHECK (qty > 0),
  unit_price numeric(20,6) NOT NULL CHECK (unit_price >= 0),
  amount numeric(20,6) NOT NULL CHECK (amount >= 0),
  PRIMARY KEY (tenant_id, submission_id, line_no),
  UNIQUE (tenant_id, submission_id, receipt_id, receipt_line_no),
  FOREIGN KEY (tenant_id, submission_id) REFERENCES receiving_invoice_submission (tenant_id, id),
  FOREIGN KEY (tenant_id, receipt_id, receipt_line_no) REFERENCES receiving_receipt_line (tenant_id, receipt_id, line_no)
);
CREATE INDEX IF NOT EXISTS receiving_invoice_submission_line_receipt_ix ON receiving_invoice_submission_line (tenant_id, receipt_id, receipt_line_no);

DO $$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['receiving_invoice_submission', 'receiving_invoice_submission_line'] LOOP
    IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE schemaname = current_schema() AND tablename = t AND policyname = t || '_tenant_access') THEN
      EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
      EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
      EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)', t || '_tenant_access', t);
      EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))', t || '_legal_entity_scope', t);
    END IF;
  END LOOP;
END
$$;

GRANT SELECT, INSERT ON receiving_invoice_submission_line TO passage_app;
GRANT SELECT, INSERT ON receiving_invoice_submission TO passage_app;
GRANT UPDATE (status, decided_by, decided_at, decision_note, invoice_id) ON receiving_invoice_submission TO passage_app;
