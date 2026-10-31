CREATE TABLE IF NOT EXISTS receiving_ers_price_confirmation (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  receipt_id uuid NOT NULL,
  line_no int NOT NULL,
  confirmed_price numeric(20,6) NOT NULL CHECK (confirmed_price >= 0),
  confirmed_by text NOT NULL,
  confirmed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, receipt_id, line_no)
);

DO $$
DECLARE
  t text := 'receiving_ers_price_confirmation';
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE schemaname = current_schema() AND tablename = t AND policyname = t || '_tenant_access') THEN
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)', t || '_tenant_access', t);
    EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))', t || '_legal_entity_scope', t);
  END IF;
END
$$;

ALTER TABLE receiving_receipt ADD COLUMN IF NOT EXISTS posted_by text NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS receiving_ers_price_confirmation_history (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL DEFAULT gen_random_uuid(),
  legal_entity_id uuid NOT NULL,
  receipt_id uuid NOT NULL,
  line_no int NOT NULL,
  old_price numeric(20,6),
  new_price numeric(20,6) NOT NULL,
  confirmed_by text NOT NULL,
  confirmed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id)
);

DO $$
DECLARE
  t text := 'receiving_ers_price_confirmation_history';
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE schemaname = current_schema() AND tablename = t AND policyname = t || '_tenant_access') THEN
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)', t || '_tenant_access', t);
    EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))', t || '_legal_entity_scope', t);
  END IF;
END
$$;

GRANT SELECT, INSERT, UPDATE ON receiving_ers_price_confirmation TO passage_app;
GRANT SELECT, INSERT ON receiving_ers_price_confirmation_history TO passage_app;
