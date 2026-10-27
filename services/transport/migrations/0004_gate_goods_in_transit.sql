-- Wave 34 slice 4: driver check-in and check-out with an append-only gate log
-- (FR-34.14, BR-34.13, BR-34.14) and goods-in-transit orders opened at the
-- Incoterm transfer event (FR-34.12, BR-34.11).
-- Every table has tenant_id first and row-level security. Amounts are
-- numeric(20,6); commands sent with an Idempotency-Key store the key and a
-- request fingerprint, as in 0001.

-- One truck or container visit (DriverVisit). driver_name and driver_license_id
-- are personal data: kept gate_pii_retention_days (default 90), then
-- pseudonymized by the gate PII job (NOT-IMPLEMENTED.md).
CREATE TABLE IF NOT EXISTS gate_visit (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  site_id uuid NOT NULL,
  direction text NOT NULL CHECK (direction IN ('INBOUND', 'OUTBOUND')),
  load_id uuid,
  carrier_scac text NOT NULL CHECK (carrier_scac ~ '^[A-Z]{2,4}$'),
  trailer_no text NOT NULL CHECK (char_length(trailer_no) BETWEEN 1 AND 20),
  seal_no text CHECK (char_length(seal_no) BETWEEN 1 AND 20),
  driver_name text NOT NULL CHECK (char_length(driver_name) BETWEEN 1 AND 100),
  driver_license_id text NOT NULL CHECK (char_length(driver_license_id) BETWEEN 1 AND 32),
  appointment_id uuid,
  scheduled_at timestamptz,
  arrival_status text CHECK (arrival_status IN ('ON_TIME', 'EARLY', 'LATE')),
  arrival_delta_min integer,
  checked_in_at timestamptz NOT NULL,
  checked_out_at timestamptz,
  free_time_min integer NOT NULL CHECK (free_time_min BETWEEN 0 AND 1440),
  status text NOT NULL CHECK (status IN ('IN_YARD', 'CHECKED_OUT')),
  dwell_min integer CHECK (dwell_min >= 0),
  detention_min integer CHECK (detention_min >= 0),
  detention_hours integer CHECK (detention_hours >= 0),
  detention_charge numeric(20,6) CHECK (detention_charge >= 0),
  version integer NOT NULL DEFAULT 1,
  idempotency_key text,
  request_hash text,
  -- The check-out's own Idempotency-Key, so a retried check-out returns the visit.
  checkout_idempotency_key text,
  checkout_request_hash text,
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  UNIQUE (tenant_id, checkout_idempotency_key),
  -- An appointment visit is classified against its scheduled time (BR-34.14).
  CHECK ((appointment_id IS NULL) = (scheduled_at IS NULL)),
  CHECK ((appointment_id IS NULL) = (arrival_status IS NULL)),
  CHECK ((status = 'CHECKED_OUT') = (checked_out_at IS NOT NULL)),
  CHECK (checked_out_at IS NULL OR checked_out_at >= checked_in_at)
);
-- A trailer is in a site's yard at most once at a time.
CREATE UNIQUE INDEX IF NOT EXISTS gate_visit_in_yard ON gate_visit (tenant_id, site_id, carrier_scac, trailer_no)
  WHERE status = 'IN_YARD';
CREATE INDEX IF NOT EXISTS gate_visit_site_checked_in ON gate_visit (tenant_id, site_id, checked_in_at DESC);
CREATE INDEX IF NOT EXISTS gate_visit_checked_in ON gate_visit (tenant_id, checked_in_at DESC);
ALTER TABLE gate_visit ENABLE ROW LEVEL SECURITY;
ALTER TABLE gate_visit FORCE ROW LEVEL SECURITY;
CREATE POLICY gate_visit_tenant_access ON gate_visit AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- The gate log (GateEvent): check-in, seal, check-out and every blocked
-- check-out attempt (AC-34.12). Append-only: the application role may only
-- insert and read, and a trigger refuses updates and deletes from any role.
-- detail carries no driver PII.
CREATE TABLE IF NOT EXISTS gate_event (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  visit_id uuid NOT NULL,
  site_id uuid NOT NULL,
  kind text NOT NULL CHECK (kind IN ('CHECK_IN', 'SEALED', 'CHECK_OUT', 'CHECK_OUT_BLOCKED')),
  occurred_at timestamptz NOT NULL,
  recorded_by text NOT NULL,
  detail jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, visit_id) REFERENCES gate_visit (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS gate_event_visit ON gate_event (tenant_id, visit_id, created_at);
CREATE INDEX IF NOT EXISTS gate_event_site ON gate_event (tenant_id, site_id, occurred_at);
CREATE OR REPLACE FUNCTION gate_event_append_only() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'gate_event is append-only';
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS gate_event_no_change ON gate_event;
CREATE TRIGGER gate_event_no_change BEFORE UPDATE OR DELETE ON gate_event
  FOR EACH ROW EXECUTE FUNCTION gate_event_append_only();
ALTER TABLE gate_event ENABLE ROW LEVEL SECURITY;
ALTER TABLE gate_event FORCE ROW LEVEL SECURITY;
CREATE POLICY gate_event_tenant_access ON gate_event AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Goods-in-transit order (GoodsInTransitOrder): receipt lines of a voyage that
-- move to the in-transit warehouse at an origin transfer event (BR-34.11).
-- lines holds each receipt line's value, quantity and estimated landed cost per
-- cost type as decimal strings.
CREATE TABLE IF NOT EXISTS goods_in_transit_order (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  voyage_id uuid NOT NULL,
  site_id uuid NOT NULL,
  incoterm text NOT NULL CHECK (incoterm IN ('EXW', 'FCA', 'FAS', 'FOB', 'CFR', 'CIF', 'CPT', 'CIP')),
  transfer_event text NOT NULL CHECK (transfer_event = 'ORIGIN_SHIPMENT'),
  in_transit_warehouse text NOT NULL CHECK (char_length(in_transit_warehouse) BETWEEN 1 AND 60),
  currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  status text NOT NULL CHECK (status IN ('PENDING', 'IN_TRANSIT')),
  lines jsonb NOT NULL,
  goods_value numeric(20,6) NOT NULL CHECK (goods_value >= 0),
  estimated_total numeric(20,6) NOT NULL CHECK (estimated_total >= 0),
  shipped_at timestamptz,
  shipped_by text,
  version integer NOT NULL DEFAULT 1,
  idempotency_key text,
  request_hash text,
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, voyage_id) REFERENCES voyage (tenant_id, id),
  CHECK ((status = 'IN_TRANSIT') = (shipped_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS goods_in_transit_order_voyage ON goods_in_transit_order (tenant_id, voyage_id);
CREATE INDEX IF NOT EXISTS goods_in_transit_order_created ON goods_in_transit_order (tenant_id, created_at DESC);
ALTER TABLE goods_in_transit_order ENABLE ROW LEVEL SECURITY;
ALTER TABLE goods_in_transit_order FORCE ROW LEVEL SECURITY;
CREATE POLICY goods_in_transit_order_tenant_access ON goods_in_transit_order AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- A voyage receipt line is on at most one goods-in-transit order (the primary
-- key), so its ownership cannot move twice, also under concurrent requests.
CREATE TABLE IF NOT EXISTS goods_in_transit_line (
  tenant_id uuid NOT NULL,
  voyage_id uuid NOT NULL,
  receipt_line integer NOT NULL CHECK (receipt_line >= 1),
  order_id uuid NOT NULL,
  PRIMARY KEY (tenant_id, voyage_id, receipt_line),
  FOREIGN KEY (tenant_id, order_id) REFERENCES goods_in_transit_order (tenant_id, id)
);
ALTER TABLE goods_in_transit_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE goods_in_transit_line FORCE ROW LEVEL SECURITY;
CREATE POLICY goods_in_transit_line_tenant_access ON goods_in_transit_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON gate_visit, goods_in_transit_order, goods_in_transit_line TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON gate_event TO passage_app';
  END IF;
END
$$;
