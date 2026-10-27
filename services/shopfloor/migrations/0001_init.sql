-- Shop floor time and attendance core (PSG-25 slice 1): workers, time
-- registrations, calculated worker-days with pay lines, idempotency records and
-- the transactional outbox. Every table is tenant scoped with forced RLS.

CREATE TABLE IF NOT EXISTS worker (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  party_id uuid NOT NULL,
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  badge_id text CHECK (char_length(badge_id) BETWEEN 1 AND 64),
  -- Identity-provider user the worker signs in as; approval of one's own day is
  -- refused (segregation of duties, PRD 4.25.17).
  user_id text CHECK (char_length(user_id) BETWEEN 1 AND 200),
  cost_rate numeric(20,6) NOT NULL CHECK (cost_rate >= 0),
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  active boolean NOT NULL DEFAULT true,
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, badge_id)
);
CREATE INDEX IF NOT EXISTS worker_name ON worker (tenant_id, name, id);
ALTER TABLE worker ENABLE ROW LEVEL SECURITY;
ALTER TABLE worker FORCE ROW LEVEL SECURITY;
CREATE POLICY worker_tenant_access ON worker AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS time_registration (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  worker_id uuid NOT NULL,
  kind text NOT NULL CHECK (kind IN ('clock-in', 'clock-out', 'job-start', 'job-stop', 'absence', 'indirect')),
  job_id uuid,
  bundle_id uuid,
  qty_good numeric(18,4) NOT NULL DEFAULT 0 CHECK (qty_good >= 0),
  qty_scrap numeric(18,4) NOT NULL DEFAULT 0 CHECK (qty_scrap >= 0),
  scrap_reason_id text,
  started_at timestamptz NOT NULL,
  stopped_at timestamptz,
  server_received_at timestamptz NOT NULL DEFAULT now(),
  device_id text,
  offline boolean NOT NULL DEFAULT false,
  -- Elapsed hours of a closed registration (stopped_at - started_at).
  hours numeric(10,4) CHECK (hours >= 0 AND hours <= 24),
  note text CHECK (char_length(note) <= 500),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, worker_id) REFERENCES worker (tenant_id, id),
  CHECK (stopped_at IS NULL OR stopped_at >= started_at),
  CHECK ((stopped_at IS NULL) = (hours IS NULL)),
  CHECK (qty_scrap = 0 OR scrap_reason_id IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS time_registration_worker_started ON time_registration (tenant_id, worker_id, started_at);
CREATE INDEX IF NOT EXISTS time_registration_job ON time_registration (tenant_id, job_id);
ALTER TABLE time_registration ENABLE ROW LEVEL SECURITY;
ALTER TABLE time_registration FORCE ROW LEVEL SECURITY;
CREATE POLICY time_registration_tenant_access ON time_registration AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS worker_day (
  tenant_id uuid NOT NULL,
  worker_id uuid NOT NULL,
  work_date date NOT NULL,
  hours numeric(10,4) NOT NULL CHECK (hours >= 0 AND hours <= 24),
  flags text[] NOT NULL DEFAULT '{}',
  -- IANA zone the day was calculated in; approval uses it to find registrations
  -- that arrived after the calculation.
  time_zone text NOT NULL CHECK (char_length(time_zone) BETWEEN 1 AND 64),
  status text NOT NULL CHECK (status IN ('calculated', 'approved')),
  approved_by text,
  approved_at timestamptz,
  calculated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, worker_id, work_date),
  FOREIGN KEY (tenant_id, worker_id) REFERENCES worker (tenant_id, id),
  CHECK ((status = 'approved') = (approved_at IS NOT NULL))
);
ALTER TABLE worker_day ENABLE ROW LEVEL SECURITY;
ALTER TABLE worker_day FORCE ROW LEVEL SECURITY;
CREATE POLICY worker_day_tenant_access ON worker_day AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS pay_line (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  worker_id uuid NOT NULL,
  work_date date NOT NULL,
  pay_type text NOT NULL CHECK (pay_type IN ('base', 'overtime-1.5', 'overtime-2.0', 'shift-premium', 'flex-in', 'flex-out', 'absence-paid', 'absence-unpaid')),
  hours numeric(10,2) NOT NULL CHECK (hours > 0),
  rate_multiplier numeric(9,6) NOT NULL CHECK (rate_multiplier > 0),
  amount numeric(20,6) NOT NULL,
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  source_registration_ids uuid[] NOT NULL DEFAULT '{}',
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, worker_id, work_date) REFERENCES worker_day (tenant_id, worker_id, work_date)
);
ALTER TABLE pay_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE pay_line FORCE ROW LEVEL SECURITY;
CREATE POLICY pay_line_tenant_access ON pay_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- BR-25.8 / FR-25.8: approved pay lines are immutable. Corrections are new lines
-- plus a reversal in a later slice; until then the database refuses changes.
CREATE OR REPLACE FUNCTION shopfloor_pay_line_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  t uuid;
  w uuid;
  d date;
BEGIN
  IF TG_OP = 'DELETE' THEN
    t := OLD.tenant_id; w := OLD.worker_id; d := OLD.work_date;
  ELSE
    t := NEW.tenant_id; w := NEW.worker_id; d := NEW.work_date;
  END IF;
  IF EXISTS (SELECT 1 FROM worker_day
             WHERE tenant_id = t AND worker_id = w AND work_date = d AND status = 'approved') THEN
    RAISE EXCEPTION 'approved pay lines are immutable' USING ERRCODE = 'check_violation';
  END IF;
  IF TG_OP = 'DELETE' THEN
    RETURN OLD;
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER pay_line_guard BEFORE INSERT OR UPDATE OR DELETE ON pay_line
  FOR EACH ROW EXECUTE FUNCTION shopfloor_pay_line_guard();

CREATE TABLE IF NOT EXISTS outbox (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL,
  aggregate_type text NOT NULL,
  aggregate_id text NOT NULL,
  type text NOT NULL,
  payload jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox FORCE ROW LEVEL SECURITY;
CREATE POLICY outbox_tenant_access ON outbox AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE INDEX IF NOT EXISTS worker_day_date ON worker_day (tenant_id, work_date, worker_id);

-- Replay records for commands sent with an Idempotency-Key (same shape as
-- dbx.IdempotencyDDL).
CREATE TABLE IF NOT EXISTS idempotency_keys (
  tenant_id uuid NOT NULL,
  key text NOT NULL,
  request_hash text NOT NULL,
  response_status int NOT NULL,
  response_body jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, key)
);
ALTER TABLE idempotency_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE idempotency_keys FORCE ROW LEVEL SECURITY;
CREATE POLICY idempotency_keys_tenant_access ON idempotency_keys AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- The application role reads and writes through RLS; it never owns the tables.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON worker, time_registration, worker_day, pay_line, idempotency_keys, outbox TO passage_app';
  END IF;
END
$$;
