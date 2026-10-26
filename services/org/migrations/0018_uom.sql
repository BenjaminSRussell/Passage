CREATE TABLE IF NOT EXISTS unit_of_measure (
  tenant_id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Za-z0-9/%]{1,10}$'),
  name text NOT NULL,
  class text NOT NULL CHECK (class IN ('quantity', 'each', 'mass', 'length', 'volume', 'area', 'time', 'temperature', 'energy', 'pressure', 'power', 'speed')),
  decimal_precision smallint NOT NULL CHECK (decimal_precision BETWEEN 0 AND 6),
  system_flag boolean GENERATED ALWAYS AS (tenant_id = '00000000-0000-0000-0000-000000000000'::uuid) STORED,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  PRIMARY KEY (tenant_id, code)
);
ALTER TABLE unit_of_measure ENABLE ROW LEVEL SECURITY;
ALTER TABLE unit_of_measure FORCE ROW LEVEL SECURITY;
CREATE POLICY unit_of_measure_tenant_access ON unit_of_measure AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY unit_of_measure_sys_read ON unit_of_measure AS PERMISSIVE FOR SELECT
  USING (tenant_id = '00000000-0000-0000-0000-000000000000'::uuid);

-- No foreign keys from from_code or to_code to unit_of_measure: a later tenant-owned
-- conversion between two sys units would break a same-tenant foreign key, so the seed
-- loader and the later store validate the codes.
CREATE TABLE IF NOT EXISTS uom_conversion (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL DEFAULT gen_random_uuid(),
  from_code text NOT NULL,
  to_code text NOT NULL,
  factor numeric(28,12) NOT NULL CHECK (factor > 0),
  offset_value numeric(28,12) NOT NULL DEFAULT 0,
  product_id uuid,
  PRIMARY KEY (tenant_id, id),
  CHECK (from_code <> to_code)
);
CREATE UNIQUE INDEX IF NOT EXISTS uom_conversion_unordered_pair ON uom_conversion
  (tenant_id, least(from_code, to_code), greatest(from_code, to_code), coalesce(product_id, '00000000-0000-0000-0000-000000000000'::uuid));
ALTER TABLE uom_conversion ENABLE ROW LEVEL SECURITY;
ALTER TABLE uom_conversion FORCE ROW LEVEL SECURITY;
CREATE POLICY uom_conversion_tenant_access ON uom_conversion AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY uom_conversion_sys_read ON uom_conversion AS PERMISSIVE FOR SELECT
  USING (tenant_id = '00000000-0000-0000-0000-000000000000'::uuid);
