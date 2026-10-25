CREATE TABLE IF NOT EXISTS bom_version (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  product_id text NOT NULL,
  site_id text NOT NULL DEFAULT '',
  effective_from date NOT NULL,
  effective_to date,
  from_qty numeric(28,12),
  to_qty numeric(28,12),
  status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'active', 'expired', 'superseded')),
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CHECK (effective_to IS NULL OR effective_to >= effective_from)
);
CREATE INDEX IF NOT EXISTS "IX-ENG-01" ON bom_version (tenant_id, product_id, status);
CREATE TABLE IF NOT EXISTS bom_line (
  tenant_id uuid NOT NULL,
  bom_version_id uuid NOT NULL,
  line_no integer NOT NULL,
  component_product_id text NOT NULL,
  quantity numeric(28,12) NOT NULL CHECK (quantity > 0),
  series numeric(28,12) NOT NULL DEFAULT 1 CHECK (series > 0),
  line_type text NOT NULL DEFAULT 'item' CHECK (line_type IN ('item', 'phantom', 'co_product', 'by_product')),
  scrap_const numeric(28,12) NOT NULL DEFAULT 0 CHECK (scrap_const >= 0),
  scrap_var_pct numeric(9,6) NOT NULL DEFAULT 0 CHECK (scrap_var_pct >= 0 AND scrap_var_pct <= 100),
  round_up text NOT NULL DEFAULT '' CHECK (round_up IN ('', 'quantity')),
  effective_from date,
  effective_to date,
  PRIMARY KEY (tenant_id, bom_version_id, line_no),
  FOREIGN KEY (tenant_id, bom_version_id) REFERENCES bom_version (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS "IX-ENG-02" ON bom_line (tenant_id, component_product_id);
CREATE TABLE IF NOT EXISTS eco (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  number text NOT NULL,
  state text NOT NULL CHECK (state IN ('draft', 'submitted', 'approved', 'scheduled', 'released', 'rejected', 'cancelled')),
  release_date date,
  decided_by text NOT NULL DEFAULT '',
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, number)
);
CREATE TABLE IF NOT EXISTS eco_replacement (
  tenant_id uuid NOT NULL,
  eco_id uuid NOT NULL,
  old_version_id uuid NOT NULL,
  new_version_id uuid NOT NULL,
  PRIMARY KEY (tenant_id, eco_id, new_version_id),
  FOREIGN KEY (tenant_id, eco_id) REFERENCES eco (tenant_id, id),
  FOREIGN KEY (tenant_id, old_version_id) REFERENCES bom_version (tenant_id, id),
  FOREIGN KEY (tenant_id, new_version_id) REFERENCES bom_version (tenant_id, id)
);
CREATE TABLE IF NOT EXISTS idempotency_keys (
  tenant_id uuid NOT NULL,
  key text NOT NULL,
  request_hash text NOT NULL,
  response_status int NOT NULL,
  response_body jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, key)
);
CREATE TABLE IF NOT EXISTS outbox (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL,
  aggregate_type text NOT NULL,
  aggregate_id text NOT NULL,
  type text NOT NULL,
  payload jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
DO $$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['bom_version', 'bom_line', 'eco', 'eco_replacement', 'idempotency_keys', 'outbox'] LOOP
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
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON bom_version, bom_line, eco, eco_replacement, idempotency_keys, outbox TO passage_app';
  END IF;
END
$$;
