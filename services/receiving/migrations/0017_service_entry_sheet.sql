CREATE TABLE IF NOT EXISTS receiving_service_entry_sheet (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  vendor_id uuid NOT NULL,
  purchase_order_id uuid NOT NULL,
  currency char(3) NOT NULL,
  status text NOT NULL CHECK (status IN ('PENDING', 'ACCEPTED', 'REJECTED')),
  total numeric NOT NULL CHECK (total >= 0),
  lines jsonb NOT NULL,
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  decided_by text,
  decided_at timestamptz,
  reject_reason text,
  PRIMARY KEY (tenant_id, id),
  CHECK ((status = 'PENDING') = (decided_by IS NULL))
);

CREATE INDEX IF NOT EXISTS receiving_service_entry_sheet_po_ix ON receiving_service_entry_sheet (tenant_id, purchase_order_id);

DO $$
DECLARE
  t text := 'receiving_service_entry_sheet';
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE schemaname = current_schema() AND tablename = t AND policyname = t || '_tenant_access') THEN
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)', t || '_tenant_access', t);
    EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))', t || '_legal_entity_scope', t);
  END IF;
END
$$;

CREATE OR REPLACE FUNCTION receiving_service_sheet_status_guard() RETURNS trigger AS $fn$
BEGIN
  IF OLD.status <> 'PENDING' THEN
    RAISE EXCEPTION 'service entry sheet in status % cannot be updated', OLD.status USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.status = 'PENDING' THEN
    RAISE EXCEPTION 'service entry sheet cannot return to PENDING' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END
$fn$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS receiving_service_sheet_status_guard ON receiving_service_entry_sheet;
CREATE TRIGGER receiving_service_sheet_status_guard BEFORE UPDATE ON receiving_service_entry_sheet
  FOR EACH ROW EXECUTE FUNCTION receiving_service_sheet_status_guard();

GRANT SELECT, INSERT ON receiving_service_entry_sheet TO passage_app;
GRANT UPDATE (status, decided_by, decided_at, reject_reason) ON receiving_service_entry_sheet TO passage_app;
