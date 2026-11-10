CREATE TABLE IF NOT EXISTS receiving_asn (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  vendor_id uuid NOT NULL,
  purchase_order_id uuid NOT NULL,
  status text NOT NULL CHECK (status IN ('OPEN', 'RECEIVING')),
  carrier text,
  tracking_number text,
  ship_date date,
  expected_arrival date,
  lines jsonb NOT NULL,
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  receipt_id uuid,
  PRIMARY KEY (tenant_id, id),
  CHECK ((status = 'OPEN') = (receipt_id IS NULL))
);

CREATE INDEX IF NOT EXISTS receiving_asn_po_ix ON receiving_asn (tenant_id, purchase_order_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS receiving_asn_vendor_ix ON receiving_asn (tenant_id, vendor_id, created_at DESC, id DESC);
CREATE UNIQUE INDEX IF NOT EXISTS receiving_asn_tracking_ux ON receiving_asn (tenant_id, vendor_id, tracking_number) WHERE tracking_number IS NOT NULL;

DO $$
DECLARE
  t text := 'receiving_asn';
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE schemaname = current_schema() AND tablename = t AND policyname = t || '_tenant_access') THEN
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)', t || '_tenant_access', t);
    EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))', t || '_legal_entity_scope', t);
  END IF;
END
$$;

CREATE OR REPLACE FUNCTION receiving_asn_status_guard() RETURNS trigger AS $fn$
BEGIN
  IF OLD.status <> 'OPEN' THEN
    RAISE EXCEPTION 'advance ship notice in status % cannot be updated', OLD.status USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.status <> 'RECEIVING' THEN
    RAISE EXCEPTION 'advance ship notice can only move to RECEIVING' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END
$fn$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS receiving_asn_status_guard ON receiving_asn;
CREATE TRIGGER receiving_asn_status_guard BEFORE UPDATE ON receiving_asn
  FOR EACH ROW EXECUTE FUNCTION receiving_asn_status_guard();

GRANT SELECT, INSERT ON receiving_asn TO passage_app;
GRANT UPDATE (status, receipt_id) ON receiving_asn TO passage_app;
