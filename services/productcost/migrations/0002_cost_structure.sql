-- Wave 39 slice 5: the first persisted costing records. Cost groups map to the
-- cost components of the split (FR-39.1); the Idempotency-Key store
-- (dbx.IdempotencyDDL) and the transactional outbox (dbx.OutboxDDL, tenant_id
-- first) serve the create and lifecycle commands.
ALTER TABLE cost_component ADD COLUMN IF NOT EXISTS created_at timestamptz NOT NULL DEFAULT now();

CREATE TABLE IF NOT EXISTS cost_group (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (char_length(code) BETWEEN 1 AND 20),
  name text NOT NULL,
  type text NOT NULL CHECK (type IN ('DirectMaterial', 'DirectManufacturing', 'Indirect', 'Undefined')),
  behaviour text NOT NULL DEFAULT 'Variable' CHECK (behaviour IN ('Fixed', 'Variable')),
  component_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code),
  FOREIGN KEY (tenant_id, component_id) REFERENCES cost_component (tenant_id, id)
);
ALTER TABLE cost_group ENABLE ROW LEVEL SECURITY;
ALTER TABLE cost_group FORCE ROW LEVEL SECURITY;
CREATE POLICY cost_group_tenant_access ON cost_group AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Keyset listing of a legal entity's versions (UUIDv7 ids sort by creation).
CREATE INDEX IF NOT EXISTS costing_version_by_legal_entity ON costing_version (tenant_id, legal_entity_id, id);

CREATE TABLE IF NOT EXISTS idempotency_keys (
  tenant_id uuid NOT NULL,
  key text NOT NULL,
  request_hash text NOT NULL,
  response_status int NOT NULL,
  response_body jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, key)
);
ALTER TABLE idempotency_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE idempotency_keys FORCE ROW LEVEL SECURITY;
CREATE POLICY idempotency_keys_tenant_access ON idempotency_keys AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS outbox (
  tenant_id uuid NOT NULL,
  id text PRIMARY KEY,
  aggregate_type text NOT NULL,
  aggregate_id text NOT NULL,
  type text NOT NULL,
  payload jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE outbox REPLICA IDENTITY FULL;
ALTER TABLE outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox FORCE ROW LEVEL SECURITY;
CREATE POLICY outbox_tenant_access ON outbox AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
