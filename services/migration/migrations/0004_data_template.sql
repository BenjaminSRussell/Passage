-- Data templates (FR-10.18): immutable per (entity, name, version) default values applied before mapping.
-- checksum is the SHA-256 of the canonical template JSON so an identical re-upload is a no-op.
CREATE TABLE migration.data_template (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  entity text NOT NULL,
  name text NOT NULL,
  version integer NOT NULL CHECK (version >= 1),
  checksum text NOT NULL,
  default_count integer NOT NULL CHECK (default_count >= 1),
  defaults jsonb NOT NULL,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, entity, name, version),
  UNIQUE (tenant_id, idempotency_key)
);

ALTER TABLE migration.data_template ENABLE ROW LEVEL SECURITY;
ALTER TABLE migration.data_template FORCE ROW LEVEL SECURITY;
CREATE POLICY data_template_tenant_access ON migration.data_template AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY data_template_legal_entity_scope ON migration.data_template AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*');

-- A run pins the template it used; the ledger records which targets each row took from it.
ALTER TABLE migration.run ADD COLUMN data_template_id uuid;
ALTER TABLE migration.run ADD CONSTRAINT run_data_template_fk
  FOREIGN KEY (tenant_id, data_template_id) REFERENCES migration.data_template (tenant_id, id);
ALTER TABLE migration.run_ledger ADD COLUMN template_fields text[] NOT NULL DEFAULT '{}';

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    GRANT SELECT, INSERT ON migration.data_template TO passage_app;
  END IF;
END
$$;
