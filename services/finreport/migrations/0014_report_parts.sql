-- PSG-1512 (wave 15, slice 12): designer parts (FR-15.2, FR-15.3, PRD §2.15.5
-- /row-sets and /column-sets). A row set is a named list of report rows, a
-- column set a named list of columns (comparative templates are column sets the
-- service ships and a tenant saves under its own code). One row per code; body
-- holds the part as the API returns it. Replacing a part is revision-checked
-- with If-Match.

CREATE TABLE IF NOT EXISTS report_row_set (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z][A-Z0-9-]{1,29}$'),
  revision integer NOT NULL DEFAULT 1 CHECK (revision >= 1),
  body jsonb NOT NULL,
  updated_by text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code)
);
ALTER TABLE report_row_set ENABLE ROW LEVEL SECURITY;
ALTER TABLE report_row_set FORCE ROW LEVEL SECURITY;
CREATE POLICY report_row_set_tenant_access ON report_row_set AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS report_column_set (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z][A-Z0-9-]{1,29}$'),
  revision integer NOT NULL DEFAULT 1 CHECK (revision >= 1),
  body jsonb NOT NULL,
  updated_by text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code)
);
ALTER TABLE report_column_set ENABLE ROW LEVEL SECURITY;
ALTER TABLE report_column_set FORCE ROW LEVEL SECURITY;
CREATE POLICY report_column_set_tenant_access ON report_column_set AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- The store inserts a part, row-locks it (FOR UPDATE) and updates it on replace; it never deletes.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON report_row_set, report_column_set TO passage_app';
  END IF;
END
$$;
