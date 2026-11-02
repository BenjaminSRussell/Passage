CREATE TABLE IF NOT EXISTS receiving_invoice_capture (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  source text NOT NULL CHECK (source IN ('UBL', 'OCR')),
  status text NOT NULL CHECK (status IN ('CAPTURED', 'NEEDS_REVIEW')),
  document_sha256 text NOT NULL,
  vendor_tax_id text NOT NULL DEFAULT '',
  invoice_number text NOT NULL,
  invoice_date date NOT NULL,
  currency char(3) NOT NULL,
  total numeric NOT NULL CHECK (total >= 0),
  confidence numeric NOT NULL CHECK (confidence >= 0 AND confidence <= 1),
  review_reasons jsonb NOT NULL DEFAULT '[]',
  lines jsonb NOT NULL,
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, document_sha256)
);

DO $$
DECLARE
  t text := 'receiving_invoice_capture';
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE schemaname = current_schema() AND tablename = t AND policyname = t || '_tenant_access') THEN
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)', t || '_tenant_access', t);
    EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))', t || '_legal_entity_scope', t);
  END IF;
END
$$;

GRANT SELECT, INSERT ON receiving_invoice_capture TO passage_app;
