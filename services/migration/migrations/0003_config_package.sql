-- Signed configuration data packages (FR-10.15) and copy-project key rewrite rules (FR-10.14).
-- A package row is written once when it is applied; the applied history is appended to in place.
CREATE TABLE migration.config_package (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  version varchar(20) NOT NULL,
  schema_version integer NOT NULL CHECK (schema_version >= 1),
  manifest jsonb NOT NULL,
  signature bytea NOT NULL,
  created_by text NOT NULL DEFAULT '',
  idempotency_key text,
  applied jsonb NOT NULL DEFAULT '[]'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key)
);

CREATE INDEX config_package_created_idx ON migration.config_package (tenant_id, created_at DESC);

CREATE TABLE migration.key_rewrite_rule (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  project_id uuid NOT NULL,
  field text NOT NULL,
  from_value text NOT NULL CHECK (from_value <> ''),
  to_value text NOT NULL,
  position integer NOT NULL DEFAULT 0,
  PRIMARY KEY (tenant_id, id)
);

CREATE INDEX key_rewrite_rule_project_idx ON migration.key_rewrite_rule (tenant_id, project_id, position);

ALTER TABLE migration.config_package ENABLE ROW LEVEL SECURITY;
ALTER TABLE migration.config_package FORCE ROW LEVEL SECURITY;
CREATE POLICY config_package_tenant_access ON migration.config_package AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY config_package_legal_entity_scope ON migration.config_package AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*');

ALTER TABLE migration.key_rewrite_rule ENABLE ROW LEVEL SECURITY;
ALTER TABLE migration.key_rewrite_rule FORCE ROW LEVEL SECURITY;
CREATE POLICY key_rewrite_rule_tenant_access ON migration.key_rewrite_rule AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY key_rewrite_rule_legal_entity_scope ON migration.key_rewrite_rule AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*');

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    -- UPDATE only on config_package: the store appends to its applied history.
    GRANT SELECT, INSERT, UPDATE ON migration.config_package TO passage_app;
    GRANT SELECT, INSERT ON migration.key_rewrite_rule TO passage_app;
  END IF;
END
$$;
