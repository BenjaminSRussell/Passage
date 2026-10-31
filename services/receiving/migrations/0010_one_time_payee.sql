CREATE TABLE IF NOT EXISTS receiving_one_time_payee (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  country char(2) NOT NULL,
  name_sealed bytea NOT NULL,
  address_sealed bytea NOT NULL,
  tax_id_sealed bytea,
  email_sealed bytea,
  bank_sealed bytea,
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id)
);

DO $$
DECLARE
  t text := 'receiving_one_time_payee';
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE schemaname = current_schema() AND tablename = t AND policyname = t || '_tenant_access') THEN
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)', t || '_tenant_access', t);
    EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))', t || '_legal_entity_scope', t);
  END IF;
END
$$;

ALTER TABLE receiving_vendor_invoice ADD COLUMN IF NOT EXISTS one_time_payee_id uuid;

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'receiving_vendor_invoice_payee_fk') THEN
    ALTER TABLE receiving_vendor_invoice ADD CONSTRAINT receiving_vendor_invoice_payee_fk
      FOREIGN KEY (tenant_id, one_time_payee_id) REFERENCES receiving_one_time_payee (tenant_id, id);
  END IF;
END
$$;

GRANT SELECT, INSERT ON receiving_one_time_payee TO passage_app;
