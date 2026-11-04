-- Field service, slice 7 (PSG-4507): technicians, work-order scheduling data,
-- manual assignment with the BR-45.3 hard constraints, the dispatch board and
-- publishing a territory-day (FR-45.7, FR-45.6). Skills are free codes until wave
-- 42 owns them.
CREATE TABLE IF NOT EXISTS service_technician (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{1,19}$'),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  worker_id uuid,
  legal_entity_id uuid NOT NULL,
  territory_ids uuid[] NOT NULL CHECK (cardinality(territory_ids) BETWEEN 1 AND 50),
  skills text[] NOT NULL DEFAULT '{}' CHECK (cardinality(skills) <= 40),
  time_zone text NOT NULL,
  -- ISO weekdays the technician works, Monday = 1.
  shift_days integer[] NOT NULL DEFAULT '{1,2,3,4,5}' CHECK (cardinality(shift_days) BETWEEN 1 AND 7 AND shift_days <@ ARRAY[1,2,3,4,5,6,7]),
  shift_start_minute integer NOT NULL DEFAULT 480 CHECK (shift_start_minute BETWEEN 0 AND 1439),
  shift_end_minute integer NOT NULL DEFAULT 1020 CHECK (shift_end_minute BETWEEN 1 AND 1440),
  home_lat numeric(9, 6) CHECK (home_lat BETWEEN -90 AND 90),
  home_lon numeric(9, 6) CHECK (home_lon BETWEEN -180 AND 180),
  truck_location_id uuid,
  max_daily_minutes integer NOT NULL DEFAULT 480 CHECK (max_daily_minutes BETWEEN 60 AND 1440),
  overtime_allowed boolean NOT NULL DEFAULT false,
  active boolean NOT NULL DEFAULT true,
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  idempotency_key text,
  request_hash text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code),
  UNIQUE (tenant_id, idempotency_key),
  CHECK (shift_start_minute < shift_end_minute),
  CHECK ((home_lat IS NULL) = (home_lon IS NULL))
);
CREATE INDEX IF NOT EXISTS service_technician_territories ON service_technician USING gin (territory_ids);
CREATE INDEX IF NOT EXISTS service_technician_recent ON service_technician (tenant_id, created_at DESC, id DESC);
ALTER TABLE service_technician ENABLE ROW LEVEL SECURITY;
ALTER TABLE service_technician FORCE ROW LEVEL SECURITY;
CREATE POLICY service_technician_tenant_access ON service_technician AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY service_technician_legal_entity_scope ON service_technician AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Scheduling data of a work order (PRD 8.45.4) and its one active assignment.
ALTER TABLE service_work_order ADD COLUMN IF NOT EXISTS required_skills text[] NOT NULL DEFAULT '{}';
ALTER TABLE service_work_order ADD COLUMN IF NOT EXISTS window_start timestamptz;
ALTER TABLE service_work_order ADD COLUMN IF NOT EXISTS window_end timestamptz;
ALTER TABLE service_work_order ADD COLUMN IF NOT EXISTS address text NOT NULL DEFAULT '';
ALTER TABLE service_work_order ADD COLUMN IF NOT EXISTS geo_lat numeric(9, 6);
ALTER TABLE service_work_order ADD COLUMN IF NOT EXISTS geo_lon numeric(9, 6);
ALTER TABLE service_work_order ADD COLUMN IF NOT EXISTS technician_id uuid;
ALTER TABLE service_work_order ADD COLUMN IF NOT EXISTS assigned_start timestamptz;
ALTER TABLE service_work_order ADD COLUMN IF NOT EXISTS assigned_end timestamptz;
ALTER TABLE service_work_order ADD COLUMN IF NOT EXISTS assignment_pinned boolean NOT NULL DEFAULT false;
ALTER TABLE service_work_order ADD CONSTRAINT service_work_order_window_ck
  CHECK ((window_start IS NULL) = (window_end IS NULL) AND (window_start IS NULL OR window_end > window_start));
ALTER TABLE service_work_order ADD CONSTRAINT service_work_order_geo_ck
  CHECK ((geo_lat IS NULL) = (geo_lon IS NULL) AND (geo_lat IS NULL OR (geo_lat BETWEEN -90 AND 90 AND geo_lon BETWEEN -180 AND 180)));
ALTER TABLE service_work_order ADD CONSTRAINT service_work_order_assignment_ck
  CHECK ((technician_id IS NULL) = (assigned_start IS NULL) AND (technician_id IS NULL) = (assigned_end IS NULL)
         AND (assigned_start IS NULL OR assigned_end > assigned_start));
ALTER TABLE service_work_order ADD CONSTRAINT service_work_order_skills_ck CHECK (cardinality(required_skills) <= 30);
ALTER TABLE service_work_order ADD CONSTRAINT service_work_order_technician_fk
  FOREIGN KEY (tenant_id, technician_id) REFERENCES service_technician (tenant_id, id);
CREATE INDEX IF NOT EXISTS service_work_order_technician ON service_work_order (tenant_id, technician_id, assigned_start)
  WHERE technician_id IS NOT NULL;

-- A published territory-day (the dispatch run of PRD 8.45.4 without a solver yet).
CREATE TABLE IF NOT EXISTS service_dispatch_run (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  territory_id uuid NOT NULL,
  run_date date NOT NULL,
  time_zone text NOT NULL,
  status text NOT NULL DEFAULT 'PUBLISHED' CHECK (status IN ('PUBLISHED')),
  assignments jsonb NOT NULL DEFAULT '[]',
  dispatched_count integer NOT NULL CHECK (dispatched_count >= 1),
  published_by text NOT NULL DEFAULT '',
  published_at timestamptz NOT NULL,
  idempotency_key text,
  request_hash text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key)
);
CREATE INDEX IF NOT EXISTS service_dispatch_run_territory ON service_dispatch_run (tenant_id, territory_id, run_date);
CREATE INDEX IF NOT EXISTS service_dispatch_run_recent ON service_dispatch_run (tenant_id, created_at DESC, id DESC);
ALTER TABLE service_dispatch_run ENABLE ROW LEVEL SECURITY;
ALTER TABLE service_dispatch_run FORCE ROW LEVEL SECURITY;
CREATE POLICY service_dispatch_run_tenant_access ON service_dispatch_run AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY service_dispatch_run_legal_entity_scope ON service_dispatch_run AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Application role privileges, in the style of 0002. Technicians are updated (edits,
-- and locked while an assignment is checked); dispatch runs are append-only;
-- service_work_order keeps the privileges it has. Nothing is deleted. Skipped when
-- the role does not exist (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON service_technician TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON service_dispatch_run TO passage_app';
  END IF;
END
$$;
