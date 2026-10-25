CREATE TABLE IF NOT EXISTS warehouse (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  site_id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,9}$'),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  type text NOT NULL DEFAULT 'standard' CHECK (type IN ('standard', 'transit', 'quarantine', 'subcontractor')),
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
  FOREIGN KEY (tenant_id, legal_entity_id, site_id) REFERENCES site (tenant_id, legal_entity_id, id)
);
CREATE INDEX IF NOT EXISTS warehouse_site ON warehouse (tenant_id, site_id);
ALTER TABLE warehouse ENABLE ROW LEVEL SECURITY;
ALTER TABLE warehouse FORCE ROW LEVEL SECURITY;
CREATE POLICY warehouse_tenant_access ON warehouse AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY warehouse_legal_entity_scope ON warehouse AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
