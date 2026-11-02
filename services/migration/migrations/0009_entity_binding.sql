-- Entity bindings (PSG-10 integration track S2): which table, query, endpoint or file of a connection profile feeds
-- which Passage entity, with the key fields that identify a source row and the optional change cursor delta sync
-- (S4) will read. The source object is validated per profile kind by the service. A binding is disabled, never deleted.
CREATE TABLE migration.entity_binding (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  connection_profile_id uuid NOT NULL,
  entity text NOT NULL CHECK (entity ~ '^[a-z][a-z0-9_.\-]{0,63}$'),
  source jsonb NOT NULL CHECK (jsonb_typeof(source) = 'object'),
  key_fields jsonb NOT NULL CHECK (jsonb_typeof(key_fields) = 'array' AND jsonb_array_length(key_fields) BETWEEN 1 AND 5),
  cursor_field text NOT NULL DEFAULT '',
  cursor_type text NOT NULL DEFAULT '' CHECK (cursor_type IN ('', 'timestamp', 'int', 'rowversion')),
  mapping_spec_id uuid,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  version integer NOT NULL DEFAULT 1,
  created_by text NOT NULL DEFAULT '',
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, connection_profile_id, entity),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, connection_profile_id) REFERENCES migration.connection_profile (tenant_id, id),
  FOREIGN KEY (tenant_id, mapping_spec_id) REFERENCES migration.mapping_spec (tenant_id, id),
  CHECK ((cursor_field = '') = (cursor_type = ''))
);

CREATE INDEX entity_binding_entity_idx ON migration.entity_binding (tenant_id, entity, id);
CREATE INDEX entity_binding_profile_idx ON migration.entity_binding (tenant_id, connection_profile_id, entity, id);

ALTER TABLE migration.entity_binding ENABLE ROW LEVEL SECURITY;
ALTER TABLE migration.entity_binding FORCE ROW LEVEL SECURITY;
CREATE POLICY entity_binding_tenant_access ON migration.entity_binding AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY entity_binding_legal_entity_scope ON migration.entity_binding AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*');

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    -- UPDATE: the store edits a binding under its version. No DELETE: bindings are disabled, not removed.
    GRANT SELECT, INSERT, UPDATE ON migration.entity_binding TO passage_app;
  END IF;
END
$$;
