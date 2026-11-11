-- Field service, issue #45 (technician day, capture and truck stock): the photos and
-- signature a device uploads, the checklist of a visit, the signature's position and the
-- technician's truck as an inventory address (FR-45.8, FR-45.9, BR-45.7).

-- Two more kinds in the append-only action log: a photo action (up to five captures) and a
-- checklist tick.
ALTER TABLE service_wo_action DROP CONSTRAINT IF EXISTS service_wo_action_kind_check;
ALTER TABLE service_wo_action ADD CONSTRAINT service_wo_action_kind_check
  CHECK (kind IN ('TRAVEL_START', 'WORK_START', 'HOLD', 'RESUME', 'PART_USED', 'SIGNATURE', 'NOTE', 'COMPLETE', 'PHOTO', 'CHECKLIST'));

-- Checklist items of a visit ([{id, text, required}]); what is ticked is read from the log.
ALTER TABLE service_work_order ADD COLUMN IF NOT EXISTS checklist jsonb NOT NULL DEFAULT '[]';
ALTER TABLE service_work_order ADD CONSTRAINT service_work_order_checklist_ck
  CHECK (jsonb_typeof(checklist) = 'array' AND jsonb_array_length(checklist) <= 40);

-- Where the customer signed (the PNG carries its own time through signed_at).
ALTER TABLE service_work_order ADD COLUMN IF NOT EXISTS signed_lat numeric(9, 6);
ALTER TABLE service_work_order ADD COLUMN IF NOT EXISTS signed_lon numeric(9, 6);
ALTER TABLE service_work_order ADD CONSTRAINT service_work_order_signed_geo_ck
  CHECK ((signed_lat IS NULL) = (signed_lon IS NULL) AND (signed_lat IS NULL OR (signed_lat BETWEEN -90 AND 90 AND signed_lon BETWEEN -180 AND 180)));

-- Uploaded images. The id is chosen by the device so an upload retried after a lost answer
-- stores nothing twice; the bytes are kept here until an object store takes them over.
CREATE TABLE IF NOT EXISTS service_wo_capture (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  work_order_id uuid NOT NULL,
  technician_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  kind text NOT NULL CHECK (kind IN ('PHOTO', 'SIGNATURE')),
  content_type text NOT NULL CHECK (content_type IN ('image/png', 'image/jpeg')),
  width_px integer NOT NULL CHECK (width_px BETWEEN 1 AND 2000),
  height_px integer NOT NULL CHECK (height_px BETWEEN 1 AND 2000),
  byte_size integer NOT NULL CHECK (byte_size BETWEEN 1 AND 2097152),
  sha256 text NOT NULL CHECK (char_length(sha256) = 64),
  data bytea NOT NULL,
  captured_at timestamptz NOT NULL,
  lat numeric(9, 6),
  lon numeric(9, 6),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, work_order_id) REFERENCES service_work_order (tenant_id, id),
  FOREIGN KEY (tenant_id, technician_id) REFERENCES service_technician (tenant_id, id),
  CHECK ((lat IS NULL) = (lon IS NULL) AND (lat IS NULL OR (lat BETWEEN -90 AND 90 AND lon BETWEEN -180 AND 180)))
);
CREATE INDEX IF NOT EXISTS service_wo_capture_order ON service_wo_capture (tenant_id, work_order_id, captured_at);
ALTER TABLE service_wo_capture ENABLE ROW LEVEL SECURITY;
ALTER TABLE service_wo_capture FORCE ROW LEVEL SECURITY;
CREATE POLICY service_wo_capture_tenant_access ON service_wo_capture AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY service_wo_capture_legal_entity_scope ON service_wo_capture AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- The truck as an inventory address: site and warehouse, plus the location inside it when
-- the truck is a location of a warehouse (truck_location_id, as before). The completed
-- event carries them so inventory can issue the consumed parts from the truck (BR-45.7).
ALTER TABLE service_technician ADD COLUMN IF NOT EXISTS truck_site_id uuid;
ALTER TABLE service_technician ADD COLUMN IF NOT EXISTS truck_warehouse_id uuid;
ALTER TABLE service_technician ADD CONSTRAINT service_technician_truck_ck
  CHECK ((truck_site_id IS NULL) = (truck_warehouse_id IS NULL) AND (truck_location_id IS NULL OR truck_site_id IS NOT NULL)) NOT VALID;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT ON service_wo_capture TO passage_app';
  END IF;
END
$$;
