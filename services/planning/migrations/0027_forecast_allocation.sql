-- FR-22.8, BR-22.10: allocation keys. A key splits an aggregate forecast (a product
-- family entered in a forecast unit of measure) across items by percentage share, and
-- converts each item's share to its inventory UoM with a per-item factor. Keys are
-- shared planning setup like coverage_group, so they stay tenant-level; the forecast
-- cells an allocation writes belong to the forecast model's legal entity.
CREATE TABLE IF NOT EXISTS forecast_allocation_key (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{1,19}$'),
  name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
  forecast_uom text NOT NULL CHECK (forecast_uom ~ '^[A-Za-z0-9._/-]{1,10}$'),
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  idempotency_key text,
  input_hash text,
  created_by text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CONSTRAINT forecast_allocation_key_code_key UNIQUE (tenant_id, code),
  CONSTRAINT forecast_allocation_key_idempotency_key UNIQUE (tenant_id, idempotency_key),
  CHECK ((idempotency_key IS NULL) = (input_hash IS NULL))
);
ALTER TABLE forecast_allocation_key ENABLE ROW LEVEL SECURITY;
ALTER TABLE forecast_allocation_key FORCE ROW LEVEL SECURITY;
CREATE POLICY forecast_allocation_key_tenant_access ON forecast_allocation_key AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- share is a percentage of the aggregate (the lines of a key sum to 100); uom_factor
-- is the item's inventory units per one forecast unit.
CREATE TABLE IF NOT EXISTS forecast_allocation_key_line (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  key_id uuid NOT NULL,
  item_id uuid NOT NULL,
  share numeric(9,6) NOT NULL CHECK (share > 0 AND share <= 100),
  uom_factor numeric(18,9) NOT NULL DEFAULT 1 CHECK (uom_factor > 0),
  PRIMARY KEY (tenant_id, id),
  CONSTRAINT forecast_allocation_key_line_item_key UNIQUE (tenant_id, key_id, item_id),
  FOREIGN KEY (tenant_id, key_id) REFERENCES forecast_allocation_key (tenant_id, id)
);
ALTER TABLE forecast_allocation_key_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE forecast_allocation_key_line FORCE ROW LEVEL SECURITY;
CREATE POLICY forecast_allocation_key_line_tenant_access ON forecast_allocation_key_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Application role privileges, in the style of 0010_grants.sql. A key is updated in
-- place (version, name, unit); its lines are replaced on update, so they need DELETE.
-- Skipped when the role does not exist (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON forecast_allocation_key TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON forecast_allocation_key_line TO passage_app';
  END IF;
END
$$;
