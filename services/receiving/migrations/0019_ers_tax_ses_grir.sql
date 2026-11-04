ALTER TABLE receiving_receipt
  ADD COLUMN IF NOT EXISTS source text NOT NULL DEFAULT 'PRODUCT';

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'receiving_receipt_source_ck') THEN
    ALTER TABLE receiving_receipt ADD CONSTRAINT receiving_receipt_source_ck CHECK (source IN ('PRODUCT', 'SERVICE_ENTRY_SHEET'));
  END IF;
END
$$;

ALTER TABLE receiving_service_entry_sheet
  ADD COLUMN IF NOT EXISTS receipt_id uuid,
  ADD COLUMN IF NOT EXISTS ledger_entry_id text;

ALTER TABLE receiving_vendor_invoice
  ADD COLUMN IF NOT EXISTS tax_amount numeric(20,6) NOT NULL DEFAULT 0 CHECK (tax_amount >= 0),
  ADD COLUMN IF NOT EXISTS tax_code text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS supply_date date,
  ADD COLUMN IF NOT EXISTS tax_rate_pct numeric(9,4) NOT NULL DEFAULT 0 CHECK (tax_rate_pct >= 0);

ALTER TABLE receiving_vendor_invoice_line
  ADD COLUMN IF NOT EXISTS service boolean NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS receiving_ers_self_bill (
  tenant_id uuid NOT NULL,
  invoice_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  vendor_id uuid NOT NULL,
  invoice_number text NOT NULL,
  document_number text NOT NULL,
  content_type text NOT NULL,
  body bytea NOT NULL,
  tax_code text NOT NULL DEFAULT '',
  tax_amount numeric(20,6) NOT NULL DEFAULT 0,
  total numeric(20,6) NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, invoice_id),
  CONSTRAINT receiving_ers_self_bill_number_uq UNIQUE (tenant_id, legal_entity_id, document_number)
);

CREATE TABLE IF NOT EXISTS receiving_self_bill_counter (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  last_no integer NOT NULL,
  PRIMARY KEY (tenant_id, legal_entity_id)
);

DO $$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['receiving_ers_self_bill', 'receiving_self_bill_counter'] LOOP
  IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE schemaname = current_schema() AND tablename = t AND policyname = t || '_tenant_access') THEN
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)', t || '_tenant_access', t);
    EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))', t || '_legal_entity_scope', t);
  END IF;
  END LOOP;
END
$$;

GRANT SELECT, INSERT ON receiving_ers_self_bill TO passage_app;
GRANT SELECT, INSERT, UPDATE ON receiving_self_bill_counter TO passage_app;
GRANT UPDATE (receipt_id, ledger_entry_id) ON receiving_service_entry_sheet TO passage_app;
