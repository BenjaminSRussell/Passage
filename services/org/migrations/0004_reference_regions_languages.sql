CREATE TABLE IF NOT EXISTS region (
  tenant_id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z]{2}-[A-Z0-9]{1,3}$'),
  country_code char(2) NOT NULL,
  name text NOT NULL,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  PRIMARY KEY (tenant_id, code),
  CHECK (left(code, 2) = country_code),
  FOREIGN KEY (tenant_id, country_code) REFERENCES country (tenant_id, code)
);
ALTER TABLE region ENABLE ROW LEVEL SECURITY;
ALTER TABLE region FORCE ROW LEVEL SECURITY;
CREATE POLICY region_tenant_access ON region AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY region_sys_read ON region AS PERMISSIVE FOR SELECT
  USING (tenant_id = '00000000-0000-0000-0000-000000000000'::uuid);

CREATE TABLE IF NOT EXISTS language (
  tenant_id uuid NOT NULL,
  code char(2) NOT NULL CHECK (code ~ '^[a-z]{2}$'),
  name text NOT NULL,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  PRIMARY KEY (tenant_id, code)
);
ALTER TABLE language ENABLE ROW LEVEL SECURITY;
ALTER TABLE language FORCE ROW LEVEL SECURITY;
CREATE POLICY language_tenant_access ON language AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY language_sys_read ON language AS PERMISSIVE FOR SELECT
  USING (tenant_id = '00000000-0000-0000-0000-000000000000'::uuid);
