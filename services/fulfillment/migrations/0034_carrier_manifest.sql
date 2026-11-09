-- Fulfillment slice 27: the carrier manifest (end-of-day close-out, FR-28.6
-- and section 5.28.10). A manifest lists the labelled parcels of one carrier
-- that left one warehouse on one day, so the carrier collects exactly what the
-- warehouse says it hands over. A parcel is one label bought for a shipment
-- (the LABELLED tracking event, provider and tracking number); it is on at most
-- one CLOSED manifest. A manifest the carrier has collected from (a parcel in
-- transit or delivered) cannot be cancelled. Cancelling a manifest frees its parcels for the next one
-- (the lines stay, inactive, as history). MF- numbers are gap-free per legal
-- entity.
CREATE TABLE IF NOT EXISTS carrier_manifest (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  manifest_number text NOT NULL,
  legal_entity_id uuid NOT NULL,
  warehouse_id uuid NOT NULL,
  carrier text NOT NULL CHECK (carrier ~ '^[a-z0-9-]{2,32}$'),
  pickup_date date NOT NULL,
  status text NOT NULL DEFAULT 'CLOSED' CHECK (status IN ('CLOSED', 'CANCELLED')),
  provider_manifest_id text CHECK (provider_manifest_id IS NULL OR length(provider_manifest_id) BETWEEN 1 AND 128),
  parcel_count integer NOT NULL CHECK (parcel_count > 0),
  shipment_count integer NOT NULL CHECK (shipment_count > 0),
  version integer NOT NULL DEFAULT 1,
  created_by text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  cancelled_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, manifest_number),
  CHECK ((status = 'CANCELLED') = (cancelled_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS carrier_manifest_lookup_idx ON carrier_manifest (tenant_id, warehouse_id, carrier, pickup_date DESC);

CREATE TABLE IF NOT EXISTS carrier_manifest_line (
  tenant_id uuid NOT NULL,
  manifest_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  shipment_id uuid NOT NULL,
  provider text NOT NULL,
  tracking_number text NOT NULL,
  shipment_number text NOT NULL,
  order_id uuid NOT NULL,
  active boolean NOT NULL DEFAULT true,
  PRIMARY KEY (tenant_id, manifest_id, shipment_id, provider, tracking_number),
  FOREIGN KEY (tenant_id, manifest_id) REFERENCES carrier_manifest (tenant_id, id),
  FOREIGN KEY (tenant_id, shipment_id) REFERENCES shipment (tenant_id, id)
);
-- A parcel is on one active manifest at a time.
CREATE UNIQUE INDEX IF NOT EXISTS carrier_manifest_line_parcel_uq ON carrier_manifest_line (tenant_id, shipment_id, provider, tracking_number) WHERE active;
-- The unmanifested-parcel query reads the labels of the last 30 days; this
-- partial index keeps it to that slice of the tracking history.
CREATE INDEX IF NOT EXISTS shipment_tracking_event_labelled_idx ON shipment_tracking_event (tenant_id, event_time) WHERE status = 'LABELLED';

ALTER TABLE carrier_manifest ENABLE ROW LEVEL SECURITY;
ALTER TABLE carrier_manifest FORCE ROW LEVEL SECURITY;
CREATE POLICY carrier_manifest_tenant_access ON carrier_manifest AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY carrier_manifest_legal_entity_scope ON carrier_manifest AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE carrier_manifest_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE carrier_manifest_line FORCE ROW LEVEL SECURITY;
CREATE POLICY carrier_manifest_line_tenant_access ON carrier_manifest_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY carrier_manifest_line_legal_entity_scope ON carrier_manifest_line AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON carrier_manifest, carrier_manifest_line TO passage_app';
  END IF;
END
$$;
