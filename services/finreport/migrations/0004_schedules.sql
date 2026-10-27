-- PSG-1503 (wave 15, slice 3): distribution schedules (FR-15.8). One row per
-- schedule code; body holds the schedule as the API returns it. Replacing a
-- schedule is revision-checked with If-Match.

CREATE TABLE IF NOT EXISTS report_schedule (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z][A-Z0-9-]{1,29}$'),
  definition_code text NOT NULL,
  enabled boolean NOT NULL,
  revision integer NOT NULL DEFAULT 1 CHECK (revision >= 1),
  body jsonb NOT NULL,
  updated_by text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code)
);
ALTER TABLE report_schedule ENABLE ROW LEVEL SECURITY;
ALTER TABLE report_schedule FORCE ROW LEVEL SECURITY;
CREATE POLICY report_schedule_tenant_access ON report_schedule AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
