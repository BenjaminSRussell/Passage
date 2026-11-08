-- Legal-entity copy (FR-6.15): one row per copy, the idempotency anchor and the audit of what
-- was copied. request_hash refuses an Idempotency-Key replayed with another body. Only structure
-- is copied; the row counts record what the copy created.
CREATE TABLE IF NOT EXISTS legal_entity_copy (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  source_id uuid NOT NULL,
  target_id uuid NOT NULL,
  idempotency_key text,
  request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
  sites integer NOT NULL DEFAULT 0 CHECK (sites >= 0),
  warehouses integer NOT NULL DEFAULT 0 CHECK (warehouses >= 0),
  locations integer NOT NULL DEFAULT 0 CHECK (locations >= 0),
  rounding_rules integer NOT NULL DEFAULT 0 CHECK (rounding_rules >= 0),
  calendar_copied boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, target_id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, source_id) REFERENCES legal_entity (tenant_id, id),
  FOREIGN KEY (tenant_id, target_id) REFERENCES legal_entity (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS legal_entity_copy_source ON legal_entity_copy (tenant_id, source_id);
ALTER TABLE legal_entity_copy ENABLE ROW LEVEL SECURITY;
ALTER TABLE legal_entity_copy FORCE ROW LEVEL SECURITY;
CREATE POLICY legal_entity_copy_tenant_access ON legal_entity_copy AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Application role privileges, as in 0028_grants.sql. The store only reads and inserts copy rows.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT ON legal_entity_copy TO passage_app';
  END IF;
END
$$;
