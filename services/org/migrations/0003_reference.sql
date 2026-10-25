CREATE TABLE IF NOT EXISTS country (
  tenant_id uuid NOT NULL,
  code char(2) NOT NULL CHECK (code ~ '^[A-Z]{2}$'),
  name text NOT NULL,
  region_required boolean NOT NULL DEFAULT false,
  postal_regex text,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  PRIMARY KEY (tenant_id, code)
);
ALTER TABLE country ENABLE ROW LEVEL SECURITY;
ALTER TABLE country FORCE ROW LEVEL SECURITY;
CREATE POLICY country_tenant_access ON country AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY country_sys_read ON country AS PERMISSIVE FOR SELECT
  USING (tenant_id = '00000000-0000-0000-0000-000000000000'::uuid);

CREATE TABLE IF NOT EXISTS currency (
  tenant_id uuid NOT NULL,
  code char(3) NOT NULL CHECK (code ~ '^[A-Z]{3}$'),
  numeric_code char(3) NOT NULL CHECK (numeric_code ~ '^[0-9]{3}$'),
  name text NOT NULL,
  minor_units smallint NOT NULL CHECK (minor_units BETWEEN 0 AND 4),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  PRIMARY KEY (tenant_id, code),
  UNIQUE (tenant_id, numeric_code)
);
ALTER TABLE currency ENABLE ROW LEVEL SECURITY;
ALTER TABLE currency FORCE ROW LEVEL SECURITY;
CREATE POLICY currency_tenant_access ON currency AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY currency_sys_read ON currency AS PERMISSIVE FOR SELECT
  USING (tenant_id = '00000000-0000-0000-0000-000000000000'::uuid);

CREATE TABLE IF NOT EXISTS reference_status (
  tenant_id uuid NOT NULL,
  kind text NOT NULL CHECK (kind IN ('country', 'currency', 'region', 'language')),
  code text NOT NULL,
  status text NOT NULL CHECK (status IN ('active', 'inactive')),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, kind, code)
);
ALTER TABLE reference_status ENABLE ROW LEVEL SECURITY;
ALTER TABLE reference_status FORCE ROW LEVEL SECURITY;
CREATE POLICY reference_status_tenant_access ON reference_status AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
