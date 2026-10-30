-- Versioned mapping catalogue (FR-10 mapping_spec). A (tenant, entity, version) row is immutable;
-- checksum is the SHA-256 of the canonical mapping JSON so an identical re-upload is a no-op.
CREATE TABLE migration.mapping_spec (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  entity text NOT NULL,
  version integer NOT NULL CHECK (version >= 1),
  checksum text NOT NULL,
  field_count integer NOT NULL,
  test_count integer NOT NULL,
  spec jsonb NOT NULL,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, entity, version),
  UNIQUE (tenant_id, idempotency_key)
);

ALTER TABLE migration.mapping_spec ENABLE ROW LEVEL SECURITY;
ALTER TABLE migration.mapping_spec FORCE ROW LEVEL SECURITY;
CREATE POLICY mapping_spec_tenant_access ON migration.mapping_spec AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY mapping_spec_legal_entity_scope ON migration.mapping_spec AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*');

ALTER TABLE migration.run ADD COLUMN mapping_spec_id uuid;
ALTER TABLE migration.run ADD CONSTRAINT run_mapping_spec_fk
  FOREIGN KEY (tenant_id, mapping_spec_id) REFERENCES migration.mapping_spec (tenant_id, id);
CREATE INDEX run_created_idx ON migration.run (tenant_id, created_at DESC);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    GRANT SELECT, INSERT ON migration.mapping_spec TO passage_app;
  END IF;
END
$$;
