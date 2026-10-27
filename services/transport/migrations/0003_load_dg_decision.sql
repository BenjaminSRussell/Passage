-- Wave 34 slice 3: loads built by rule (FR-34.1), dangerous-goods checks
-- (FR-34.15) and freight bill approvals and disputes (BR-34.4).
-- Every table has tenant_id first and row-level security. Quantities are
-- numeric(20,6); commands sent with an Idempotency-Key store the key and a
-- request fingerprint, as in 0001.

-- One row per rule-based build request; its loads point back to it, so a replay
-- with the same key returns the same loads.
CREATE TABLE IF NOT EXISTS load_build (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  idempotency_key text,
  request_hash text,
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key)
);
ALTER TABLE load_build ENABLE ROW LEVEL SECURITY;
ALTER TABLE load_build FORCE ROW LEVEL SECURITY;
CREATE POLICY load_build_tenant_access ON load_build AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- TMS load (§6.34.4). The id is shared with wms.Load. A rule-built load never
-- exceeds its capacity in any dimension (BR-34.8).
CREATE TABLE IF NOT EXISTS load (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  build_id uuid NOT NULL,
  build_position integer NOT NULL CHECK (build_position >= 1),
  mode text NOT NULL CHECK (mode IN ('TL', 'LTL', 'PARCEL', 'INTERMODAL')),
  status text NOT NULL DEFAULT 'PLANNED'
    CHECK (status IN ('PLANNED', 'RATED', 'TENDERED', 'ACCEPTED', 'DISPATCHED', 'IN_TRANSIT', 'DELIVERED', 'CLOSED', 'CANCELLED')),
  ship_from_site_id uuid NOT NULL,
  ship_to_region text NOT NULL CHECK (char_length(ship_to_region) BETWEEN 1 AND 20),
  pickup_date date NOT NULL,
  total_weight_kg numeric(20,6) NOT NULL CHECK (total_weight_kg >= 0),
  total_volume_m3 numeric(20,6) NOT NULL CHECK (total_volume_m3 >= 0),
  pallets integer NOT NULL CHECK (pallets >= 0),
  max_weight_kg numeric(20,6) NOT NULL CHECK (max_weight_kg > 0),
  max_volume_m3 numeric(20,6) NOT NULL CHECK (max_volume_m3 > 0),
  max_pallets integer NOT NULL CHECK (max_pallets > 0),
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, build_id, build_position),
  FOREIGN KEY (tenant_id, build_id) REFERENCES load_build (tenant_id, id),
  CHECK (total_weight_kg <= max_weight_kg AND total_volume_m3 <= max_volume_m3 AND pallets <= max_pallets)
);
CREATE INDEX IF NOT EXISTS load_status_pickup ON load (tenant_id, status, pickup_date);
CREATE INDEX IF NOT EXISTS load_created ON load (tenant_id, created_at DESC);
ALTER TABLE load ENABLE ROW LEVEL SECURITY;
ALTER TABLE load FORCE ROW LEVEL SECURITY;
CREATE POLICY load_tenant_access ON load AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- LoadShipment: a shipment is on at most one load (the primary key), which also
-- stops two concurrent builds from planning the same shipment twice.
CREATE TABLE IF NOT EXISTS load_shipment (
  tenant_id uuid NOT NULL,
  shipment_id uuid NOT NULL,
  load_id uuid NOT NULL,
  position integer NOT NULL CHECK (position >= 1),
  PRIMARY KEY (tenant_id, shipment_id),
  UNIQUE (tenant_id, load_id, position),
  FOREIGN KEY (tenant_id, load_id) REFERENCES load (tenant_id, id)
);
ALTER TABLE load_shipment ENABLE ROW LEVEL SECURITY;
ALTER TABLE load_shipment FORCE ROW LEVEL SECURITY;
CREATE POLICY load_shipment_tenant_access ON load_shipment AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- DG check results per load (DgCheckResult, the DG load report of §6.34.14).
-- Findings and shipping-paper lines are JSON with decimal-string quantities.
CREATE TABLE IF NOT EXISTS dg_check (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  load_id uuid NOT NULL,
  regulation text NOT NULL CHECK (regulation IN ('ADR', '49CFR')),
  result text NOT NULL CHECK (result IN ('PASS', 'WARN', 'FAIL')),
  adr_points numeric(20,6) NOT NULL CHECK (adr_points >= 0),
  findings jsonb NOT NULL,
  shipping_papers jsonb NOT NULL,
  checked_by text NOT NULL,
  checked_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, load_id) REFERENCES load (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS dg_check_load ON dg_check (tenant_id, load_id, checked_at DESC);
ALTER TABLE dg_check ENABLE ROW LEVEL SECURITY;
ALTER TABLE dg_check FORCE ROW LEVEL SECURITY;
CREATE POLICY dg_check_tenant_access ON dg_check AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Approvals and disputes of freight bills, append-only: who decided, when, why
-- and the state change (BR-34.4).
CREATE TABLE IF NOT EXISTS freight_bill_decision (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  freight_bill_id uuid NOT NULL,
  action text NOT NULL CHECK (action IN ('APPROVE', 'DISPUTE')),
  from_status text NOT NULL CHECK (from_status IN ('MATCHED', 'VARIANCE', 'DISPUTED')),
  to_status text NOT NULL CHECK (to_status IN ('DISPUTED', 'APPROVED')),
  reason text NOT NULL DEFAULT '' CHECK (char_length(reason) <= 500),
  decided_by text NOT NULL,
  decided_at timestamptz NOT NULL,
  idempotency_key text,
  request_hash text,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, freight_bill_id) REFERENCES freight_bill (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS freight_bill_decision_bill ON freight_bill_decision (tenant_id, freight_bill_id, decided_at);
ALTER TABLE freight_bill_decision ENABLE ROW LEVEL SECURITY;
ALTER TABLE freight_bill_decision FORCE ROW LEVEL SECURITY;
CREATE POLICY freight_bill_decision_tenant_access ON freight_bill_decision AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON load_build, load, load_shipment, dg_check, freight_bill_decision TO passage_app';
  END IF;
END
$$;
