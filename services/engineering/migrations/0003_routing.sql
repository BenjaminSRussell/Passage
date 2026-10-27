CREATE TABLE IF NOT EXISTS route_version (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  product_id text NOT NULL,
  site_id text NOT NULL,
  route_code text NOT NULL,
  version_no integer NOT NULL CHECK (version_no >= 1),
  effective_from date NOT NULL,
  effective_to date,
  status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'active', 'expired')),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, product_id, site_id, route_code, version_no),
  CHECK (effective_to IS NULL OR effective_to >= effective_from)
);
CREATE INDEX IF NOT EXISTS "IX-ENG-05" ON route_version (tenant_id, product_id, site_id, effective_from, status);
CREATE TABLE IF NOT EXISTS route_operation (
  tenant_id uuid NOT NULL,
  route_version_id uuid NOT NULL,
  operation_no integer NOT NULL,
  operation_id text NOT NULL DEFAULT '' CHECK (length(operation_id) <= 20),
  next_no integer NOT NULL DEFAULT 0 CHECK (next_no >= 0),
  link_type text NOT NULL DEFAULT 'hard' CHECK (link_type IN ('hard', 'soft')),
  priority text NOT NULL DEFAULT 'primary' CHECK (priority IN ('primary', 'secondary1', 'secondary2', 'secondary3')),
  setup_h numeric(14,4) NOT NULL DEFAULT 0 CHECK (setup_h >= 0),
  run_h numeric(14,4) NOT NULL DEFAULT 0 CHECK (run_h >= 0),
  process_qty numeric(20,6) NOT NULL DEFAULT 1 CHECK (process_qty > 0),
  queue_before_h numeric(14,4) NOT NULL DEFAULT 0 CHECK (queue_before_h >= 0),
  queue_after_h numeric(14,4) NOT NULL DEFAULT 0 CHECK (queue_after_h >= 0),
  transit_h numeric(14,4) NOT NULL DEFAULT 0 CHECK (transit_h >= 0),
  efficiency_pct numeric(9,6) NOT NULL DEFAULT 0 CHECK (efficiency_pct >= 0),
  overlap_qty numeric(20,6) NOT NULL DEFAULT 0 CHECK (overlap_qty >= 0),
  scrap_percent numeric(9,6) NOT NULL DEFAULT 0 CHECK (scrap_percent BETWEEN 0 AND 100),
  PRIMARY KEY (tenant_id, route_version_id, operation_no),
  FOREIGN KEY (tenant_id, route_version_id) REFERENCES route_version (tenant_id, id)
);
CREATE TABLE IF NOT EXISTS operation_requirement (
  tenant_id uuid NOT NULL,
  route_version_id uuid NOT NULL,
  operation_no integer NOT NULL,
  req_index integer NOT NULL,
  kind text NOT NULL CHECK (kind IN ('resource', 'group', 'capability', 'type')),
  ref_id text NOT NULL,
  level integer NOT NULL DEFAULT 0 CHECK (level >= 0),
  quantity integer NOT NULL DEFAULT 1 CHECK (quantity >= 1),
  PRIMARY KEY (tenant_id, route_version_id, operation_no, req_index),
  FOREIGN KEY (tenant_id, route_version_id, operation_no) REFERENCES route_operation (tenant_id, route_version_id, operation_no)
);
DO $$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['route_version', 'route_operation', 'operation_requirement'] LOOP
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
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON route_version, route_operation, operation_requirement TO passage_app';
  END IF;
END
$$;
