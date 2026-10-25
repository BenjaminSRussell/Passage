CREATE TABLE IF NOT EXISTS exchange_rate_type (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL,
  name text NOT NULL,
  allow_inverse boolean NOT NULL DEFAULT true,
  provider text,
  created_at timestamptz NOT NULL DEFAULT now(),
  created_by text,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code)
);
ALTER TABLE exchange_rate_type ENABLE ROW LEVEL SECURITY;
ALTER TABLE exchange_rate_type FORCE ROW LEVEL SECURITY;
CREATE POLICY exchange_rate_type_tenant_access ON exchange_rate_type AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS exchange_rate (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  rate_type_id uuid NOT NULL,
  from_currency char(3) NOT NULL,
  to_currency char(3) NOT NULL,
  valid_from date NOT NULL,
  version int NOT NULL DEFAULT 1,
  rate numeric(20,10) NOT NULL CHECK (rate > 0),
  source text NOT NULL CHECK (source IN ('manual', 'ecb', 'openexchangerates', 'csv', 'migration')),
  provider_timestamp timestamptz,
  payload_hash bytea CHECK (octet_length(payload_hash) = 32),
  superseded_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  created_by text,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, rate_type_id) REFERENCES exchange_rate_type (tenant_id, id),
  UNIQUE (tenant_id, rate_type_id, from_currency, to_currency, valid_from, version)
);
CREATE INDEX IF NOT EXISTS ix_fx_exchange_rate_current
  ON exchange_rate (tenant_id, rate_type_id, from_currency, to_currency, valid_from DESC)
  WHERE superseded_by IS NULL;
ALTER TABLE exchange_rate ENABLE ROW LEVEL SECURITY;
ALTER TABLE exchange_rate FORCE ROW LEVEL SECURITY;
CREATE POLICY exchange_rate_tenant_access ON exchange_rate AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS provider_import_run (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  provider text NOT NULL,
  rate_type_id uuid NOT NULL,
  run_date date NOT NULL,
  status text NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
  attempts int NOT NULL DEFAULT 0,
  pairs_imported int NOT NULL DEFAULT 0,
  pairs_unchanged int NOT NULL DEFAULT 0,
  error text,
  started_at timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz,
  last_success_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, rate_type_id) REFERENCES exchange_rate_type (tenant_id, id),
  UNIQUE (tenant_id, provider, rate_type_id, run_date)
);
ALTER TABLE provider_import_run ENABLE ROW LEVEL SECURITY;
ALTER TABLE provider_import_run FORCE ROW LEVEL SECURITY;
CREATE POLICY provider_import_run_tenant_access ON provider_import_run AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS outbox (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL,
  aggregate_type text NOT NULL,
  aggregate_id text NOT NULL,
  type text NOT NULL,
  payload jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox FORCE ROW LEVEL SECURITY;
CREATE POLICY outbox_tenant_access ON outbox AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
