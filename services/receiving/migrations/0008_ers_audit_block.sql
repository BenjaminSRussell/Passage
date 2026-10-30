ALTER TABLE receiving_vendor_invoice
  ADD COLUMN IF NOT EXISTS source text NOT NULL DEFAULT 'MANUAL' CHECK (source IN ('MANUAL', 'ERS')),
  ADD COLUMN IF NOT EXISTS status text NOT NULL DEFAULT 'POSTED' CHECK (status IN ('POSTED', 'AUDIT_BLOCKED')),
  ADD COLUMN IF NOT EXISTS posted_by text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS released_by text,
  ADD COLUMN IF NOT EXISTS released_at timestamptz;

ALTER TABLE receiving_vendor_invoice_line
  ADD COLUMN IF NOT EXISTS grir_cleared numeric(20,6);

CREATE TABLE IF NOT EXISTS receiving_ers_vendor (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  vendor_id uuid NOT NULL,
  enabled boolean NOT NULL,
  updated_by text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id, vendor_id)
);

CREATE TABLE IF NOT EXISTS receiving_audit_policy (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  sample_rate_pct numeric(5,2) NOT NULL CHECK (sample_rate_pct >= 0 AND sample_rate_pct <= 100),
  updated_by text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id)
);

DO $$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['receiving_ers_vendor', 'receiving_audit_policy'] LOOP
    IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE schemaname = current_schema() AND tablename = t AND policyname = t || '_tenant_access') THEN
      EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
      EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
      EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)', t || '_tenant_access', t);
      EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))', t || '_legal_entity_scope', t);
    END IF;
  END LOOP;
END
$$;

GRANT SELECT, INSERT, UPDATE ON receiving_ers_vendor, receiving_audit_policy TO passage_app;
GRANT UPDATE (ledger_entry_id, status, released_by, released_at) ON receiving_vendor_invoice TO passage_app;
