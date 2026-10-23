CREATE TABLE IF NOT EXISTS legal_entity (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9]{2,8}$'),
  name text NOT NULL,
  country char(2) NOT NULL,
  currency char(3) NOT NULL,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  version integer NOT NULL DEFAULT 1,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code),
  UNIQUE (tenant_id, idempotency_key)
);
ALTER TABLE legal_entity ENABLE ROW LEVEL SECURITY;
ALTER TABLE legal_entity FORCE ROW LEVEL SECURITY;
CREATE POLICY legal_entity_tenant_access ON legal_entity AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY legal_entity_legal_entity_scope ON legal_entity AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
