-- Wave 34 slice 5: trade-compliance screening holds (FR-34.19, BR-34.17) and
-- stored route plans (FR-34.2, BR-34.1).
-- Every table has tenant_id first and row-level security, as in 0001.

-- A load whose screen decided HOLD. Only a TRADE_COMPLIANCE_RELEASE user closes
-- it, with a reason; the release user, time and reason stay on the row (the
-- release log of AC-34.16). A load has at most one OPEN hold.
CREATE TABLE IF NOT EXISTS screening_hold (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  load_id uuid NOT NULL,
  status text NOT NULL CHECK (status IN ('OPEN', 'RELEASED')),
  parties jsonb NOT NULL CHECK (jsonb_typeof(parties) = 'array'),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  released_by text,
  release_reason text CHECK (release_reason IS NULL OR char_length(release_reason) BETWEEN 1 AND 500),
  released_at timestamptz,
  version integer NOT NULL DEFAULT 1,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, load_id) REFERENCES load (tenant_id, id),
  CHECK ((status = 'RELEASED') = (released_at IS NOT NULL AND released_by IS NOT NULL AND release_reason IS NOT NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS screening_hold_one_open ON screening_hold (tenant_id, load_id) WHERE status = 'OPEN';
CREATE INDEX IF NOT EXISTS screening_hold_status_created ON screening_hold (tenant_id, status, created_at DESC);
ALTER TABLE screening_hold ENABLE ROW LEVEL SECURITY;
ALTER TABLE screening_hold FORCE ROW LEVEL SECURITY;
CREATE POLICY screening_hold_tenant_access ON screening_hold AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- A solved route optimization. request_hash (SHA-256 of the normalized request)
-- is the idempotency key of POST /routes:optimize: the same request returns the
-- stored plan instead of solving again.
CREATE TABLE IF NOT EXISTS route_plan (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
  request jsonb NOT NULL,
  solution jsonb NOT NULL,
  stop_count integer NOT NULL CHECK (stop_count >= 1),
  vehicle_count integer NOT NULL CHECK (vehicle_count >= 1),
  route_count integer NOT NULL CHECK (route_count >= 0),
  dropped_count integer NOT NULL CHECK (dropped_count >= 0 AND dropped_count <= stop_count),
  total_distance_km numeric(20,6) NOT NULL CHECK (total_distance_km >= 0),
  time_limit_s integer NOT NULL CHECK (time_limit_s BETWEEN 5 AND 600),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, request_hash)
);
CREATE INDEX IF NOT EXISTS route_plan_created ON route_plan (tenant_id, created_at DESC);
ALTER TABLE route_plan ENABLE ROW LEVEL SECURITY;
ALTER TABLE route_plan FORCE ROW LEVEL SECURITY;
CREATE POLICY route_plan_tenant_access ON route_plan AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Holds are released, never deleted; route plans are written once.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON screening_hold TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON route_plan TO passage_app';
  END IF;
END
$$;
