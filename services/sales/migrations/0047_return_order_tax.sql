ALTER TABLE return_order ADD COLUMN IF NOT EXISTS tax numeric(20,6) NOT NULL DEFAULT 0 CHECK (tax >= 0);

CREATE TABLE IF NOT EXISTS return_order_tax (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  return_order_id uuid NOT NULL,
  original_invoice_id uuid NOT NULL,
  invoice_line_no int NOT NULL CHECK (invoice_line_no >= 1),
  tax_code text NOT NULL CHECK (char_length(tax_code) BETWEEN 1 AND 20),
  qty numeric(20,6) NOT NULL CHECK (qty > 0),
  tax numeric(20,6) NOT NULL CHECK (tax >= 0),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, return_order_id, invoice_line_no),
  FOREIGN KEY (tenant_id, return_order_id) REFERENCES return_order (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS return_order_tax_invoice ON return_order_tax (tenant_id, original_invoice_id);

ALTER TABLE return_order_tax ENABLE ROW LEVEL SECURITY;
ALTER TABLE return_order_tax FORCE ROW LEVEL SECURITY;
CREATE POLICY return_order_tax_tenant_access ON return_order_tax AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY return_order_tax_legal_entity_scope ON return_order_tax AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON return_order_tax TO passage_app';
  END IF;
END
$$;
