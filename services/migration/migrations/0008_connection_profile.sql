-- Connection profiles (PSG-10 integration track S1): how a tenant reaches a legacy or custom ERP. Non-secret settings
-- live in config (driver, host, port, database, baseUrl, auth type, paging style, codepage, timezone, drop directory);
-- the one secret (a password, token or key) is sealed with platform fieldcrypt, bound to the tenant and to
-- "migration.connection_profile.secret:"+id, and never read back through the API. Kind and source system are fixed at
-- creation; name, config and status change under optimistic versioning.
CREATE TABLE migration.connection_profile (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  name text NOT NULL CHECK (name <> '' AND char_length(name) <= 80),
  kind text NOT NULL CHECK (kind IN ('sql', 'rest', 'file')),
  source_system text NOT NULL CHECK (source_system IN ('ax2012', 'd365fo', 'sap_ecc', 'sap_s4') OR source_system ~ '^custom_[a-z0-9_]{1,40}$'),
  config jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(config) = 'object'),
  secret_sealed bytea,
  secret_key_id text,
  secret_updated_at timestamptz,
  version integer NOT NULL DEFAULT 1,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  created_by text NOT NULL DEFAULT '',
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, name),
  UNIQUE (tenant_id, idempotency_key),
  CHECK ((secret_sealed IS NULL) = (secret_key_id IS NULL) AND (secret_sealed IS NULL) = (secret_updated_at IS NULL))
);

CREATE INDEX connection_profile_name_idx ON migration.connection_profile (tenant_id, name, id);

ALTER TABLE migration.connection_profile ENABLE ROW LEVEL SECURITY;
ALTER TABLE migration.connection_profile FORCE ROW LEVEL SECURITY;
CREATE POLICY connection_profile_tenant_access ON migration.connection_profile AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY connection_profile_legal_entity_scope ON migration.connection_profile AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*');

-- Custom source systems: custom_<slug> next to the four legacy systems (run, xref and project).
ALTER TABLE migration.run DROP CONSTRAINT run_source_system_check;
ALTER TABLE migration.run ADD CONSTRAINT run_source_system_check
  CHECK (source_system IN ('ax2012', 'd365fo', 'sap_ecc', 'sap_s4') OR source_system ~ '^custom_[a-z0-9_]{1,40}$');
ALTER TABLE migration.xref DROP CONSTRAINT xref_source_system_check;
ALTER TABLE migration.xref ADD CONSTRAINT xref_source_system_check
  CHECK (source_system IN ('ax2012', 'd365fo', 'sap_ecc', 'sap_s4') OR source_system ~ '^custom_[a-z0-9_]{1,40}$');
ALTER TABLE migration.project DROP CONSTRAINT project_source_system_check;
ALTER TABLE migration.project ADD CONSTRAINT project_source_system_check
  CHECK (source_system IN ('ax2012', 'd365fo', 'sap_ecc', 'sap_s4') OR source_system ~ '^custom_[a-z0-9_]{1,40}$');

-- A migrate project names the connection profile it reads from. target_legal_entity_id is now allowed on migrate
-- projects (the legal entity the migrated data lands in); the source legal entity stays a copy-project field.
ALTER TABLE migration.project ADD COLUMN connection_profile_id uuid;
ALTER TABLE migration.project ADD CONSTRAINT project_connection_profile_fk
  FOREIGN KEY (tenant_id, connection_profile_id) REFERENCES migration.connection_profile (tenant_id, id);
CREATE INDEX project_connection_profile_idx ON migration.project (tenant_id, connection_profile_id) WHERE connection_profile_id IS NOT NULL;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    -- UPDATE: the store edits a profile under its version and sets its sealed secret. No DELETE: profiles are disabled, not removed.
    GRANT SELECT, INSERT, UPDATE ON migration.connection_profile TO passage_app;
  END IF;
END
$$;
