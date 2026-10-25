ALTER TABLE legal_entity ADD COLUMN default_language text NOT NULL DEFAULT 'en-US';
ALTER TABLE legal_entity ADD COLUMN registration_number text CHECK (char_length(registration_number) <= 40);
ALTER TABLE legal_entity ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now();

CREATE TABLE IF NOT EXISTS legal_entity_name (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  name text NOT NULL,
  valid_from timestamptz NOT NULL DEFAULT now(),
  valid_to timestamptz,
  PRIMARY KEY (tenant_id, legal_entity_id, valid_from),
  FOREIGN KEY (tenant_id, legal_entity_id) REFERENCES legal_entity (tenant_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS legal_entity_name_open ON legal_entity_name (tenant_id, legal_entity_id) WHERE valid_to IS NULL;

ALTER TABLE legal_entity NO FORCE ROW LEVEL SECURITY;
INSERT INTO legal_entity_name (tenant_id, legal_entity_id, name, valid_from)
  SELECT tenant_id, id, name, created_at FROM legal_entity ON CONFLICT DO NOTHING;
ALTER TABLE legal_entity FORCE ROW LEVEL SECURITY;

ALTER TABLE legal_entity_name ENABLE ROW LEVEL SECURITY;
ALTER TABLE legal_entity_name FORCE ROW LEVEL SECURITY;
CREATE POLICY legal_entity_name_tenant_access ON legal_entity_name AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY legal_entity_name_legal_entity_scope ON legal_entity_name AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
