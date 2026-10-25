CREATE TABLE IF NOT EXISTS site (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,9}$'),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  time_zone text NOT NULL,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  version integer NOT NULL DEFAULT 1,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, code),
  UNIQUE (tenant_id, legal_entity_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES legal_entity (tenant_id, id)
);
ALTER TABLE site ENABLE ROW LEVEL SECURITY;
ALTER TABLE site FORCE ROW LEVEL SECURITY;
CREATE POLICY site_tenant_access ON site AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY site_legal_entity_scope ON site AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
