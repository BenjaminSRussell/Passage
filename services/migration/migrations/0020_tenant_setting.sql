-- Per-tenant migration settings (FR-10.16). One row per tenant, written only when a tenant changes a default; a tenant
-- without a row runs on the built-in 15-minute minimum. `recurring_min_interval_min` is `mig.recurring.min_interval_min`:
-- the shortest gap a project or binding cron may leave between two scheduled runs (15 to 1,440 minutes).
CREATE TABLE migration.tenant_setting (
  tenant_id uuid NOT NULL,
  recurring_min_interval_min integer NOT NULL CHECK (recurring_min_interval_min BETWEEN 15 AND 1440),
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  updated_by text NOT NULL DEFAULT '',
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id)
);

ALTER TABLE migration.tenant_setting ENABLE ROW LEVEL SECURITY;
ALTER TABLE migration.tenant_setting FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_setting_tenant_access ON migration.tenant_setting AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY tenant_setting_legal_entity_scope ON migration.tenant_setting AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*');

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    -- UPDATE: the store changes the row under its version.
    GRANT SELECT, INSERT, UPDATE ON migration.tenant_setting TO passage_app;
  END IF;
END
$$;
