-- #46: every saved version of a document template, so a change can be looked back at, and the version a save was
-- based on (If-Match) can be checked against the current one.
CREATE TABLE IF NOT EXISTS print_document_template_version (
  tenant_id uuid NOT NULL,
  code text NOT NULL,
  version integer NOT NULL CHECK (version > 0),
  name text NOT NULL,
  document_type text NOT NULL,
  is_default boolean NOT NULL,
  config jsonb NOT NULL,
  saved_by text NOT NULL DEFAULT '',
  saved_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, code, version),
  FOREIGN KEY (tenant_id, code) REFERENCES print_document_template (tenant_id, code) ON DELETE CASCADE
);
ALTER TABLE print_document_template_version ENABLE ROW LEVEL SECURITY;
ALTER TABLE print_document_template_version FORCE ROW LEVEL SECURITY;
CREATE POLICY print_document_template_version_tenant_access ON print_document_template_version AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- The templates saved before this migration keep their current version as their first recorded one.
INSERT INTO print_document_template_version (tenant_id, code, version, name, document_type, is_default, config, saved_at)
SELECT tenant_id, code, version, name, document_type, is_default, config, updated_at FROM print_document_template
ON CONFLICT DO NOTHING;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON print_document_template_version TO passage_app';
  END IF;
END
$$;
