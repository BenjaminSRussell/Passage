CREATE TABLE IF NOT EXISTS receiving_invoice_adjustment (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  original_invoice_id uuid NOT NULL,
  vendor_id uuid NOT NULL,
  kind text NOT NULL CHECK (kind IN ('CREDIT', 'DEBIT')),
  document_number text NOT NULL,
  document_date date NOT NULL,
  posting_date date NOT NULL,
  currency char(3) NOT NULL,
  ledger_id uuid NOT NULL,
  fiscal_period_id uuid NOT NULL,
  ledger_entry_id text NOT NULL,
  total numeric(20,6) NOT NULL CHECK (total > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, original_invoice_id) REFERENCES receiving_vendor_invoice (tenant_id, id)
);

CREATE UNIQUE INDEX IF NOT EXISTS receiving_invoice_adjustment_number_uq ON receiving_invoice_adjustment (tenant_id, original_invoice_id, lower(document_number));

CREATE TABLE IF NOT EXISTS receiving_invoice_adjustment_line (
  tenant_id uuid NOT NULL,
  adjustment_id uuid NOT NULL,
  line_no int NOT NULL,
  legal_entity_id uuid NOT NULL,
  invoice_id uuid NOT NULL,
  invoice_line_no int NOT NULL,
  amount numeric(20,6) NOT NULL CHECK (amount > 0),
  PRIMARY KEY (tenant_id, adjustment_id, line_no),
  FOREIGN KEY (tenant_id, adjustment_id) REFERENCES receiving_invoice_adjustment (tenant_id, id),
  FOREIGN KEY (tenant_id, invoice_id, invoice_line_no) REFERENCES receiving_vendor_invoice_line (tenant_id, invoice_id, line_no)
);

CREATE INDEX IF NOT EXISTS receiving_invoice_adjustment_line_invoice_ix ON receiving_invoice_adjustment_line (tenant_id, invoice_id, invoice_line_no);

CREATE TABLE IF NOT EXISTS receiving_settlement (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  kind text NOT NULL CHECK (kind IN ('PLAN_ROW', 'CONSIGNMENT')),
  source_ref text NOT NULL,
  vendor_id text NOT NULL,
  currency char(3) NOT NULL,
  amount numeric(20,6) NOT NULL CHECK (amount > 0),
  posting_date date NOT NULL,
  ledger_id uuid NOT NULL,
  fiscal_period_id uuid NOT NULL,
  ledger_entry_id text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id)
);

CREATE UNIQUE INDEX IF NOT EXISTS receiving_settlement_source_uq ON receiving_settlement (tenant_id, kind, source_ref);

DO $$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['receiving_invoice_adjustment', 'receiving_invoice_adjustment_line', 'receiving_settlement'] LOOP
    IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE schemaname = current_schema() AND tablename = t AND policyname = t || '_tenant_access') THEN
      EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
      EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
      EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)', t || '_tenant_access', t);
      EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))', t || '_legal_entity_scope', t);
    END IF;
  END LOOP;
END
$$;

GRANT SELECT, INSERT ON receiving_invoice_adjustment, receiving_invoice_adjustment_line, receiving_settlement TO passage_app;
