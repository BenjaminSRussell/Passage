-- Wave 34 slice 2: tenders, tracking events and landed-cost voyages.
-- Every table has tenant_id first and row-level security. Amounts inside the
-- JSON documents are decimal strings, never floats; cost_total is numeric(20,6).
-- Commands sent with an Idempotency-Key store the key and a request fingerprint
-- on the row they create, as in 0001.

-- One row per tender: the ranked carriers, the cascade attempts and the outcome
-- (BR-34.3). Attempts are a JSON array because the cascade is replayed as a whole
-- under a row lock; they are never queried one by one.
CREATE TABLE IF NOT EXISTS tender (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  load_id uuid NOT NULL,
  status text NOT NULL CHECK (status IN ('PENDING', 'ACCEPTED', 'TENDER_FAILED')),
  carriers jsonb NOT NULL,
  rates jsonb NOT NULL DEFAULT '{}'::jsonb,
  currency char(3),
  timeout_seconds integer NOT NULL CHECK (timeout_seconds BETWEEN 300 AND 86400),
  attempts jsonb NOT NULL DEFAULT '[]'::jsonb,
  accepted_carrier_id uuid,
  planner_task boolean NOT NULL DEFAULT false,
  -- Deadline of the pending attempt, so the expiry sweep finds overdue tenders by index.
  pending_expires_at timestamptz,
  version integer NOT NULL DEFAULT 1,
  idempotency_key text,
  request_hash text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  CHECK ((status = 'ACCEPTED') = (accepted_carrier_id IS NOT NULL)),
  CHECK ((status = 'PENDING') = (pending_expires_at IS NOT NULL))
);
-- Invariant (§6.34.4): a load has at most one ACCEPTED tender. A load also has at
-- most one PENDING tender, so it is never offered to two cascades at once; after
-- TENDER_FAILED the planner can tender it again.
CREATE UNIQUE INDEX IF NOT EXISTS tender_one_open_per_load
  ON tender (tenant_id, load_id) WHERE status IN ('PENDING', 'ACCEPTED');
CREATE INDEX IF NOT EXISTS tender_pending_expiry
  ON tender (tenant_id, pending_expires_at) WHERE status = 'PENDING';
CREATE INDEX IF NOT EXISTS tender_created ON tender (tenant_id, created_at DESC);
ALTER TABLE tender ENABLE ROW LEVEL SECURITY;
ALTER TABLE tender FORCE ROW LEVEL SECURITY;
CREATE POLICY tender_tenant_access ON tender AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Status reports per load and stop (FR-34.6). Source and ref are the ingest
-- idempotency key (§6.34.5).
CREATE TABLE IF NOT EXISTS tracking_event (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  load_id uuid NOT NULL,
  source text NOT NULL CHECK (source IN ('EDI214', 'API', 'TELEMATICS', 'DRIVER')),
  ref text NOT NULL CHECK (char_length(ref) BETWEEN 1 AND 64),
  stop_no integer NOT NULL CHECK (stop_no BETWEEN 1 AND 999),
  code text NOT NULL CHECK (code IN ('X3', 'X1', 'AF', 'D1')),
  occurred_at timestamptz NOT NULL,
  location text NOT NULL DEFAULT '' CHECK (char_length(location) <= 100),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, source, ref)
);
CREATE INDEX IF NOT EXISTS tracking_event_load ON tracking_event (tenant_id, load_id, occurred_at);
CREATE INDEX IF NOT EXISTS tracking_event_created ON tracking_event (tenant_id, created_at DESC);
ALTER TABLE tracking_event ENABLE ROW LEVEL SECURITY;
ALTER TABLE tracking_event FORCE ROW LEVEL SECURITY;
CREATE POLICY tracking_event_tenant_access ON tracking_event AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Landed-cost voyages (FR-34.9, FR-34.12). Legs, containers, folios, cost lines,
-- receipt lines and allocations are JSON documents owned by the voyage.
CREATE TABLE IF NOT EXISTS voyage (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  reference text NOT NULL CHECK (char_length(reference) BETWEEN 1 AND 40),
  currency char(3) NOT NULL,
  status text NOT NULL CHECK (status IN ('ESTIMATED', 'POSTED')),
  legs jsonb NOT NULL,
  containers jsonb NOT NULL,
  folios jsonb NOT NULL,
  cost_lines jsonb NOT NULL,
  receipt_lines jsonb NOT NULL,
  cost_total numeric(20,6) NOT NULL CHECK (cost_total >= 0),
  cost_allocations jsonb NOT NULL DEFAULT '[]'::jsonb,
  allocations jsonb NOT NULL DEFAULT '[]'::jsonb,
  posted_at timestamptz,
  version integer NOT NULL DEFAULT 1,
  idempotency_key text,
  request_hash text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  CHECK ((status = 'POSTED') = (posted_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS voyage_status ON voyage (tenant_id, status, created_at DESC);
ALTER TABLE voyage ENABLE ROW LEVEL SECURITY;
ALTER TABLE voyage FORCE ROW LEVEL SECURITY;
CREATE POLICY voyage_tenant_access ON voyage AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON tender, tracking_event, voyage TO passage_app';
  END IF;
END
$$;
