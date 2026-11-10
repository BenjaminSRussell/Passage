ALTER TABLE rma_credit ADD COLUMN IF NOT EXISTS tax numeric(20,6) NOT NULL DEFAULT 0 CHECK (tax >= 0);

CREATE TABLE IF NOT EXISTS rma_credit_tax (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  rma_credit_id uuid NOT NULL,
  tax_code text NOT NULL CHECK (char_length(tax_code) BETWEEN 1 AND 20),
  tax_account text NOT NULL,
  amount numeric(20,6) NOT NULL CHECK (amount > 0),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, rma_credit_id, tax_code),
  FOREIGN KEY (tenant_id, rma_credit_id) REFERENCES rma_credit (tenant_id, id)
);
ALTER TABLE rma_credit_tax ENABLE ROW LEVEL SECURITY;
ALTER TABLE rma_credit_tax FORCE ROW LEVEL SECURITY;
CREATE POLICY rma_credit_tax_tenant_access ON rma_credit_tax AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY rma_credit_tax_legal_entity_scope ON rma_credit_tax AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON rma_credit_tax TO passage_app';
  END IF;
END
$$;
