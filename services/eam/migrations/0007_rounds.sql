-- Wave 44, slice 5: maintenance rounds (FR-44.14, BR-44.9). A round is an ordered
-- list of stops inspected together; a plan line of type ROUND schedules it on
-- time (BR-44.1), and each occurrence is one ROUND work order that carries a copy
-- of the stops with their outcomes. The emergency regularization and plan review
-- reminders (BR-44.7, BR-44.15) and the cost and backlog KPIs (FR-44.13) are
-- queries over existing tables and need no table of their own.

CREATE TABLE IF NOT EXISTS maintenance_round (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{1,29}$'),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  site_id uuid NOT NULL,
  active boolean NOT NULL DEFAULT true,
  idempotency_key text,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code),
  UNIQUE (tenant_id, idempotency_key)
);

-- The stops of a round. Rounds are create-only in this slice, so the stops never
-- change; work orders keep their own copy (work_order_stop) all the same.
CREATE TABLE IF NOT EXISTS round_stop (
  tenant_id uuid NOT NULL,
  round_id uuid NOT NULL,
  sequence integer NOT NULL CHECK (sequence BETWEEN 1 AND 50),
  asset_id uuid NOT NULL,
  job_type text NOT NULL CHECK (job_type ~ '^[A-Z0-9_]{2,20}$'),
  mandatory boolean NOT NULL DEFAULT false,
  travel_minutes integer NOT NULL DEFAULT 0 CHECK (travel_minutes BETWEEN 0 AND 480),
  PRIMARY KEY (tenant_id, round_id, sequence),
  FOREIGN KEY (tenant_id, round_id) REFERENCES maintenance_round (tenant_id, id),
  FOREIGN KEY (tenant_id, asset_id) REFERENCES asset (tenant_id, id)
);

-- The stops of one ROUND work order, one job per stop in stop order (BR-44.9).
-- outcome stays NULL until the stop is DONE or SKIPPED; a SKIPPED stop carries a
-- reason, and a SKIPPED mandatory stop links the ROUND request it raised.
CREATE TABLE IF NOT EXISTS work_order_stop (
  tenant_id uuid NOT NULL,
  work_order_id uuid NOT NULL,
  sequence integer NOT NULL CHECK (sequence BETWEEN 1 AND 50),
  asset_id uuid NOT NULL,
  job_type text NOT NULL CHECK (job_type ~ '^[A-Z0-9_]{2,20}$'),
  mandatory boolean NOT NULL DEFAULT false,
  travel_minutes integer NOT NULL DEFAULT 0 CHECK (travel_minutes BETWEEN 0 AND 480),
  outcome text CHECK (outcome IS NULL OR outcome IN ('DONE', 'SKIPPED')),
  reason text CHECK (reason IS NULL OR char_length(reason) BETWEEN 1 AND 120),
  request_id uuid,
  recorded_by text,
  recorded_at timestamptz,
  idempotency_key text,
  PRIMARY KEY (tenant_id, work_order_id, sequence),
  UNIQUE (tenant_id, idempotency_key),
  CHECK ((outcome IS NULL) = (recorded_at IS NULL)),
  CHECK (outcome IS DISTINCT FROM 'SKIPPED' OR reason IS NOT NULL),
  CHECK (request_id IS NULL OR (outcome = 'SKIPPED' AND mandatory)),
  FOREIGN KEY (tenant_id, work_order_id) REFERENCES work_order (tenant_id, id),
  FOREIGN KEY (tenant_id, asset_id) REFERENCES asset (tenant_id, id),
  FOREIGN KEY (tenant_id, request_id) REFERENCES maintenance_request (tenant_id, id)
);

-- Plan lines of type ROUND (FR-44.14): time only, scoped to a round. The line's
-- asset is the round's first stop (the anchor the work order is booked on), and
-- the job types live on the stops, so a ROUND line has no job type of its own.
ALTER TABLE plan_line ADD COLUMN IF NOT EXISTS round_id uuid;
ALTER TABLE plan_line ALTER COLUMN job_type DROP NOT NULL;
DO $$
DECLARE c text;
BEGIN
  -- Replace the 0001 line type and counter checks, which predate ROUND.
  FOR c IN SELECT conname FROM pg_constraint
    WHERE conrelid = 'plan_line'::regclass AND contype = 'c'
      AND (pg_get_constraintdef(oid) LIKE '%TIME_AND_COUNTER%'
        OR pg_get_constraintdef(oid) LIKE '%counter_id IS NOT NULL%'
        OR pg_get_constraintdef(oid) LIKE '%round_id%'
        OR pg_get_constraintdef(oid) LIKE '%job_type IS NOT NULL%')
  LOOP
    EXECUTE format('ALTER TABLE plan_line DROP CONSTRAINT %I', c);
  END LOOP;
  ALTER TABLE plan_line ADD CONSTRAINT plan_line_kind_check
    CHECK (line_type IN ('TIME', 'COUNTER', 'TIME_OR_COUNTER', 'TIME_AND_COUNTER', 'ROUND'));
  ALTER TABLE plan_line ADD CONSTRAINT plan_line_counter_check
    CHECK (line_type IN ('TIME', 'ROUND') OR (counter_id IS NOT NULL AND counter_interval IS NOT NULL));
  ALTER TABLE plan_line ADD CONSTRAINT plan_line_round_check
    CHECK ((line_type = 'ROUND') = (round_id IS NOT NULL)
      AND (line_type <> 'ROUND' OR (counter_id IS NULL AND counter_interval IS NULL)));
  ALTER TABLE plan_line ADD CONSTRAINT plan_line_job_check
    CHECK (line_type = 'ROUND' OR job_type IS NOT NULL);
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'plan_line'::regclass AND conname = 'plan_line_round_fk') THEN
    ALTER TABLE plan_line ADD CONSTRAINT plan_line_round_fk
      FOREIGN KEY (tenant_id, round_id) REFERENCES maintenance_round (tenant_id, id);
  END IF;
END $$;

-- Row-level security: tenant_access on every new tenant table (same shape as 0001).
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['maintenance_round', 'round_stop', 'work_order_stop'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('DROP POLICY IF EXISTS %I_tenant_access ON %I', t, t);
    EXECUTE format('CREATE POLICY %I_tenant_access ON %I AS PERMISSIVE
      USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)
      WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)', t, t);
  END LOOP;
END $$;

-- Application role privileges (same rules as 0004). Rounds and their stops are
-- create-only. A work order stop is row-locked (FOR UPDATE) and its outcome set
-- once, so it also gets UPDATE; nothing deletes stops.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON maintenance_round, round_stop TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON work_order_stop TO passage_app';
  END IF;
END
$$;
