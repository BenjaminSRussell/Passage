-- Fulfillment slice 10: batch, serial and location capture at pick (BR-28.10,
-- FUL-0006). A line released with tracking_required must have its shipped
-- quantity covered by tracking entries before the packing slip posts. An entry
-- is stamped with the packing slip it shipped on; capture replaces only the
-- unstamped (not yet shipped) entries of a line. A serial number is unique per
-- product while its shipment is live.

ALTER TABLE shipment_line ADD COLUMN IF NOT EXISTS tracking_required boolean NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS pick_tracking (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  shipment_id uuid NOT NULL,
  shipment_line_id uuid NOT NULL,
  product_id uuid NOT NULL,
  batch_no text,
  serial_no text,
  location_code text,
  qty numeric(20,6) NOT NULL CHECK (qty > 0),
  packing_slip_id uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, shipment_id) REFERENCES shipment (tenant_id, id),
  FOREIGN KEY (tenant_id, shipment_line_id) REFERENCES shipment_line (tenant_id, id),
  CHECK (batch_no IS NOT NULL OR serial_no IS NOT NULL),
  CHECK (serial_no IS NULL OR qty = 1)
);
CREATE INDEX IF NOT EXISTS pick_tracking_line_idx ON pick_tracking (tenant_id, shipment_line_id);
CREATE INDEX IF NOT EXISTS pick_tracking_shipment_idx ON pick_tracking (tenant_id, shipment_id);
CREATE UNIQUE INDEX IF NOT EXISTS pick_tracking_serial_uq ON pick_tracking (tenant_id, product_id, serial_no) WHERE serial_no IS NOT NULL;

ALTER TABLE pick_tracking ENABLE ROW LEVEL SECURITY;
ALTER TABLE pick_tracking FORCE ROW LEVEL SECURITY;
CREATE POLICY pick_tracking_tenant_access ON pick_tracking AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY pick_tracking_legal_entity_access ON pick_tracking AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON pick_tracking TO passage_app';
  END IF;
END
$$;
