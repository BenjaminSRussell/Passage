-- Migration projects (EN-064, FR-10.14): the unit a copy between legal entities hangs its key rewrite rules on.
-- Type, source system and the two legal entities are fixed at creation because xref lineage is keyed by project;
-- name, status, cutover and schedule can change under optimistic versioning. Legal entity ids reference the org
-- service by id only (no cross-service foreign key).
CREATE TABLE migration.project (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  name text NOT NULL CHECK (name <> '' AND char_length(name) <= 80),
  project_type text NOT NULL CHECK (project_type IN ('migrate', 'copy', 'config_package')),
  source_system text CHECK (source_system IN ('ax2012', 'd365fo', 'sap_ecc', 'sap_s4')),
  source_legal_entity_id uuid,
  target_legal_entity_id uuid,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
  cutover_at timestamptz,
  schedule_cron text,
  version integer NOT NULL DEFAULT 1,
  created_by text NOT NULL DEFAULT '',
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, name),
  UNIQUE (tenant_id, idempotency_key),
  CHECK (project_type <> 'copy' OR (source_legal_entity_id IS NOT NULL AND target_legal_entity_id IS NOT NULL
    AND source_legal_entity_id <> target_legal_entity_id)),
  CHECK (project_type <> 'migrate' OR source_system IS NOT NULL)
);

CREATE INDEX project_name_idx ON migration.project (tenant_id, name, id);

ALTER TABLE migration.project ENABLE ROW LEVEL SECURITY;
ALTER TABLE migration.project FORCE ROW LEVEL SECURITY;
CREATE POLICY project_tenant_access ON migration.project AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY project_legal_entity_scope ON migration.project AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*');

-- Rewrite rules belong to a project; replacing a project's rules deletes and re-inserts them. The key is checked on
-- every new or changed row. NOT VALID because a row written before 0006 would carry a project id nothing points at,
-- and this migration (run as passage_owner, NOBYPASSRLS, under FORCE RLS) can neither see nor remove such a row. No
-- route wrote rules before 0006 (only store tests did), and rules are read by project, so an orphan is unreachable;
-- VALIDATE CONSTRAINT stays an operator step (NOT-IMPLEMENTED.md).
ALTER TABLE migration.key_rewrite_rule ADD CONSTRAINT key_rewrite_rule_project_fk
  FOREIGN KEY (tenant_id, project_id) REFERENCES migration.project (tenant_id, id) NOT VALID;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    -- UPDATE: the store edits a project under its version and locks it while replacing rewrite rules.
    GRANT SELECT, INSERT, UPDATE ON migration.project TO passage_app;
    -- DELETE: replacing a project's rewrite rules removes the old set.
    GRANT DELETE ON migration.key_rewrite_rule TO passage_app;
  END IF;
END
$$;
