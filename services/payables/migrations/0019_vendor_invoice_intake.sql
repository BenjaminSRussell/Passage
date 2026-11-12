-- #35 (35-4): vendor invoices received electronically (a cXML InvoiceDetailRequest of a procurement network) wait here for an AP
-- clerk. An intake row posts nothing: the clerk matches it to receipt lines and posts the vendor invoice in receiving (GR/IR),
-- whose invoice.posted opens the AP item. One row per sender document (source, source_ref); a vendor's invoice number is taken once
-- per legal entity.
CREATE TABLE IF NOT EXISTS ap_vendor_invoice_intake (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  source text NOT NULL CHECK (source IN ('CXML')),
  source_ref text NOT NULL CHECK (char_length(source_ref) BETWEEN 1 AND 200),
  content_hash text NOT NULL,
  vendor_id uuid NOT NULL,
  invoice_number text NOT NULL CHECK (char_length(invoice_number) BETWEEN 1 AND 100),
  invoice_date date NOT NULL,
  purpose text NOT NULL DEFAULT 'standard' CHECK (purpose IN ('standard', 'creditMemo', 'debitMemo', 'lineLevelCreditMemo')),
  purchase_order_id uuid,
  purchase_order_number text NOT NULL DEFAULT '',
  currency char(3) NOT NULL,
  net numeric(20,6) NOT NULL,
  gross numeric(20,6) NOT NULL,
  lines jsonb NOT NULL,
  status text NOT NULL DEFAULT 'RECEIVED' CHECK (status IN ('RECEIVED')),
  received_by text NOT NULL,
  received_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, source, source_ref),
  UNIQUE (tenant_id, legal_entity_id, vendor_id, invoice_number)
);
CREATE INDEX IF NOT EXISTS ap_vendor_invoice_intake_list ON ap_vendor_invoice_intake (tenant_id, legal_entity_id, received_at DESC, id DESC);

ALTER TABLE ap_vendor_invoice_intake ENABLE ROW LEVEL SECURITY;
ALTER TABLE ap_vendor_invoice_intake FORCE ROW LEVEL SECURITY;
CREATE POLICY ap_vendor_invoice_intake_tenant_access ON ap_vendor_invoice_intake AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY ap_vendor_invoice_intake_legal_entity_scope ON ap_vendor_invoice_intake AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON ap_vendor_invoice_intake TO passage_app';
  END IF;
END
$$;
