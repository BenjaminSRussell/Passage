CREATE TABLE IF NOT EXISTS operating_unit (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  type text NOT NULL CHECK (type IN ('department', 'cost_center', 'business_unit', 'value_stream')),
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{1,19}$'),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  default_legal_entity_id uuid,
  manager_party_id uuid,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  version integer NOT NULL DEFAULT 1,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, type, code),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, default_legal_entity_id) REFERENCES legal_entity (tenant_id, id),
  CHECK (manager_party_id IS NULL OR type = 'cost_center')
);
CREATE INDEX IF NOT EXISTS operating_unit_default_legal_entity ON operating_unit (tenant_id, default_legal_entity_id);
ALTER TABLE operating_unit ENABLE ROW LEVEL SECURITY;
ALTER TABLE operating_unit FORCE ROW LEVEL SECURITY;
CREATE POLICY operating_unit_tenant_access ON operating_unit AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
