CREATE TABLE IF NOT EXISTS receiving_asn (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  vendor_id uuid NOT NULL,
  purchase_order_id uuid NOT NULL,
  status text NOT NULL CHECK (status IN ('OPEN', 'RECEIVING')),
  carrier text,
  tracking_number text,
  tracking_key text,
  ship_date date,
  expected_arrival date,
  lines jsonb NOT NULL,
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  receipt_id uuid,
  PRIMARY KEY (tenant_id, id),
  CHECK ((status = 'OPEN') = (receipt_id IS NULL)),
  FOREIGN KEY (tenant_id, receipt_id) REFERENCES receiving_receipt (tenant_id, id)
);

CREATE INDEX IF NOT EXISTS receiving_asn_po_ix ON receiving_asn (tenant_id, purchase_order_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS receiving_asn_vendor_ix ON receiving_asn (tenant_id, vendor_id, created_at DESC, id DESC);
CREATE UNIQUE INDEX IF NOT EXISTS receiving_asn_tracking_ux ON receiving_asn (tenant_id, legal_entity_id, vendor_id, tracking_key) WHERE tracking_key IS NOT NULL;

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
  IF OLD.status = 'OPEN' AND NEW.status = 'RECEIVING' THEN
    RETURN NEW;
  END IF;
  IF OLD.status = 'RECEIVING' AND NEW.status = 'OPEN' AND NEW.receipt_id IS NULL
     AND EXISTS (SELECT 1 FROM receiving_receipt r WHERE r.tenant_id = OLD.tenant_id AND r.id = OLD.receipt_id AND r.state = 'CANCELLED') THEN
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'advance ship notice cannot move from % to %', OLD.status, NEW.status USING ERRCODE = 'check_violation';
END
$fn$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS receiving_asn_status_guard ON receiving_asn;
CREATE TRIGGER receiving_asn_status_guard BEFORE UPDATE ON receiving_asn
  FOR EACH ROW EXECUTE FUNCTION receiving_asn_status_guard();

GRANT SELECT, INSERT ON receiving_asn TO passage_app;
GRANT UPDATE (status, receipt_id) ON receiving_asn TO passage_app;
