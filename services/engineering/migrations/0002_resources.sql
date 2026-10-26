CREATE TABLE IF NOT EXISTS work_calendar (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL,
  name text NOT NULL,
  template jsonb NOT NULL,
  closed_dates date[] NOT NULL DEFAULT '{}',
  generated_from date,
  generated_to date,
  version integer NOT NULL DEFAULT 1,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code)
);
CREATE TABLE IF NOT EXISTS calendar_day (
  tenant_id uuid NOT NULL,
  calendar_id uuid NOT NULL,
  date date NOT NULL,
  open boolean NOT NULL,
  shifts jsonb NOT NULL,
  total_h numeric(10,4) NOT NULL,
  PRIMARY KEY (tenant_id, calendar_id, date),
  FOREIGN KEY (tenant_id, calendar_id) REFERENCES work_calendar (tenant_id, id)
);
CREATE TABLE IF NOT EXISTS resource_group (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL,
  name text NOT NULL,
  site_id text NOT NULL,
  calendar_id uuid,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code),
  FOREIGN KEY (tenant_id, calendar_id) REFERENCES work_calendar (tenant_id, id)
);
CREATE TABLE IF NOT EXISTS capability (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL,
  name text NOT NULL,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code)
);
CREATE TABLE IF NOT EXISTS resource (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL,
  name text NOT NULL,
  type text NOT NULL CHECK (type IN ('machine', 'tool', 'labor', 'vendor', 'location')),
  site_id text NOT NULL,
  group_id uuid,
  calendar_id uuid,
  efficiency_pct numeric(5,2) NOT NULL DEFAULT 100,
  capacity_per_day_h numeric(10,4) NOT NULL DEFAULT 0,
  exclusive boolean NOT NULL DEFAULT false,
  vendor_party_id text,
  version integer NOT NULL DEFAULT 1,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code),
  FOREIGN KEY (tenant_id, group_id) REFERENCES resource_group (tenant_id, id),
  FOREIGN KEY (tenant_id, calendar_id) REFERENCES work_calendar (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS "IX-ENG-03" ON resource (tenant_id, group_id);
CREATE TABLE IF NOT EXISTS resource_capability (
  tenant_id uuid NOT NULL,
  resource_id uuid NOT NULL,
  capability_id uuid NOT NULL,
  effective_from date NOT NULL,
  level integer NOT NULL CHECK (level >= 1),
  effective_to date,
  PRIMARY KEY (tenant_id, resource_id, capability_id, effective_from),
  FOREIGN KEY (tenant_id, resource_id) REFERENCES resource (tenant_id, id),
  FOREIGN KEY (tenant_id, capability_id) REFERENCES capability (tenant_id, id),
  CHECK (effective_to IS NULL OR effective_to >= effective_from)
);
CREATE INDEX IF NOT EXISTS "IX-ENG-04" ON resource_capability (tenant_id, capability_id);
DO $$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['work_calendar', 'calendar_day', 'resource_group', 'capability', 'resource', 'resource_capability'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)', t || '_tenant_access', t);
  END LOOP;
END
$$;
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON work_calendar, calendar_day, resource_group, capability, resource, resource_capability TO passage_app';
  END IF;
END
$$;
