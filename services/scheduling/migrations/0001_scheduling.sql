-- Wave 23 slice 1: resources with weekly calendars, schedulable operations,
-- schedule runs, scheduled job segments (GiST exclusion against double
-- booking), idempotency records and the transactional outbox.

-- btree_gist lets the exclusion constraint compare uuid equality inside GiST.
CREATE EXTENSION IF NOT EXISTS btree_gist;

CREATE TABLE IF NOT EXISTS scheduling_resource (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,31}$'),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  finite boolean NOT NULL DEFAULT true,
  time_zone text NOT NULL CHECK (char_length(time_zone) BETWEEN 1 AND 64),
  shifts jsonb NOT NULL CHECK (jsonb_typeof(shifts) = 'array' AND jsonb_array_length(shifts) BETWEEN 1 AND 14),
  version integer NOT NULL DEFAULT 1,
  idempotency_key text,
  idempotency_fingerprint text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code),
  UNIQUE (tenant_id, idempotency_key)
);
ALTER TABLE scheduling_resource ENABLE ROW LEVEL SECURITY;
ALTER TABLE scheduling_resource FORCE ROW LEVEL SECURITY;
CREATE POLICY scheduling_resource_tenant_access ON scheduling_resource AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS scheduling_operation (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  order_id uuid NOT NULL,
  oper_number integer NOT NULL CHECK (oper_number > 0),
  minutes integer NOT NULL CHECK (minutes BETWEEN 1 AND 100000),
  due_at timestamptz,
  ready_at timestamptz,
  priority integer NOT NULL DEFAULT 100,
  candidate_resource_ids uuid[] NOT NULL CHECK (cardinality(candidate_resource_ids) >= 1),
  status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'scheduled', 'unplaced')),
  idempotency_key text,
  idempotency_fingerprint text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, order_id, oper_number),
  UNIQUE (tenant_id, idempotency_key)
);
CREATE INDEX IF NOT EXISTS scheduling_operation_order ON scheduling_operation (tenant_id, order_id);
ALTER TABLE scheduling_operation ENABLE ROW LEVEL SECURITY;
ALTER TABLE scheduling_operation FORCE ROW LEVEL SECURITY;
CREATE POLICY scheduling_operation_tenant_access ON scheduling_operation AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS schedule_run (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  scope text NOT NULL CHECK (scope IN ('order', 'selection', 'site', 'move')),
  mode text NOT NULL CHECK (mode IN ('operations', 'jobs')),
  direction text NOT NULL DEFAULT 'forward' CHECK (direction = 'forward'),
  finite boolean NOT NULL DEFAULT true,
  algorithm text NOT NULL DEFAULT 'heuristic' CHECK (algorithm = 'heuristic'),
  status text NOT NULL DEFAULT 'completed' CHECK (status = 'completed'),
  operations_count integer NOT NULL DEFAULT 0 CHECK (operations_count >= 0),
  makespan_minutes integer NOT NULL DEFAULT 0 CHECK (makespan_minutes >= 0),
  total_lateness_minutes integer NOT NULL DEFAULT 0 CHECK (total_lateness_minutes >= 0),
  solver_status text CHECK (solver_status IN ('OPTIMAL', 'FEASIBLE', 'UNKNOWN')),
  messages jsonb NOT NULL DEFAULT '[]',
  idempotency_key text,
  idempotency_fingerprint text,
  created_at timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key)
);
ALTER TABLE schedule_run ENABLE ROW LEVEL SECURITY;
ALTER TABLE schedule_run FORCE ROW LEVEL SECURITY;
CREATE POLICY schedule_run_tenant_access ON schedule_run AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- One row per contiguous segment of an operation on a resource. The exclusion
-- constraint is the last line of defence against double booking on finite resources.
CREATE TABLE IF NOT EXISTS scheduled_job (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  job_id uuid NOT NULL,
  order_id uuid NOT NULL,
  oper_number integer NOT NULL,
  resource_id uuid NOT NULL,
  segment_no integer NOT NULL CHECK (segment_no >= 1),
  start_at timestamptz NOT NULL,
  end_at timestamptz NOT NULL,
  minutes integer NOT NULL CHECK (minutes > 0),
  run_id uuid NOT NULL,
  firmed boolean NOT NULL DEFAULT false,
  finite boolean NOT NULL,
  late_minutes integer NOT NULL DEFAULT 0 CHECK (late_minutes >= 0),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, job_id, segment_no),
  CHECK (end_at > start_at),
  FOREIGN KEY (tenant_id, job_id) REFERENCES scheduling_operation (tenant_id, id),
  FOREIGN KEY (tenant_id, resource_id) REFERENCES scheduling_resource (tenant_id, id),
  FOREIGN KEY (tenant_id, run_id) REFERENCES schedule_run (tenant_id, id),
  EXCLUDE USING gist (tenant_id WITH =, resource_id WITH =, tstzrange(start_at, end_at) WITH &&) WHERE (finite)
);
CREATE INDEX IF NOT EXISTS scheduled_job_resource_start ON scheduled_job (tenant_id, resource_id, start_at);
CREATE INDEX IF NOT EXISTS scheduled_job_order ON scheduled_job (tenant_id, order_id);
ALTER TABLE scheduled_job ENABLE ROW LEVEL SECURITY;
ALTER TABLE scheduled_job FORCE ROW LEVEL SECURITY;
CREATE POLICY scheduled_job_tenant_access ON scheduled_job AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

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

-- Replay records for commands whose result is not a single stored row
-- (job moves and firming). Same shape as dbx.IdempotencyDDL.
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

CREATE INDEX IF NOT EXISTS schedule_run_created ON schedule_run (tenant_id, created_at DESC, id DESC);

-- The application role reads and writes through RLS; it never owns the tables.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON scheduling_resource, scheduling_operation, schedule_run, scheduled_job, idempotency_keys, outbox TO passage_app';
  END IF;
END
$$;
