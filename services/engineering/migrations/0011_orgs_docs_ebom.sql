-- FR-9.14–9.18 engineering organizations, categories, nomenclatures, versions, release and readiness.
-- FR-9.24–9.27 documents, Part 11 signatures and eBOM import.
CREATE TABLE IF NOT EXISTS engineering_organization (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id text NOT NULL,
  code text NOT NULL,
  name text NOT NULL,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id),
  UNIQUE (tenant_id, code)
);
CREATE TABLE IF NOT EXISTS version_nomenclature (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL,
  separator text NOT NULL DEFAULT '',
  segments jsonb NOT NULL,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code)
);
CREATE TABLE IF NOT EXISTS engineering_category (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL,
  name text NOT NULL,
  product_type text NOT NULL DEFAULT '',
  track_versions boolean NOT NULL DEFAULT true,
  version_as_dimension boolean NOT NULL DEFAULT false,
  nomenclature_id uuid,
  readiness_policy_id uuid,
  release_policy_id uuid,
  change_controlled boolean NOT NULL DEFAULT false,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code)
);
CREATE TABLE IF NOT EXISTS engineering_version (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  product_id text NOT NULL,
  version_code text NOT NULL,
  engineering_le_id text NOT NULL,
  category_id uuid,
  bom_version_id uuid,
  route_version_id uuid,
  lifecycle_state text NOT NULL DEFAULT '',
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, product_id, version_code)
);
CREATE TABLE IF NOT EXISTS release_policy (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL,
  copy_bom boolean NOT NULL DEFAULT true,
  activate_bom boolean NOT NULL DEFAULT true,
  copy_route boolean NOT NULL DEFAULT true,
  activate_route boolean NOT NULL DEFAULT true,
  copy_attributes boolean NOT NULL DEFAULT false,
  copy_order_settings boolean NOT NULL DEFAULT false,
  copy_documents boolean NOT NULL DEFAULT false,
  require_acceptance jsonb NOT NULL DEFAULT '{}',
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code)
);
CREATE TABLE IF NOT EXISTS site_mapping (
  tenant_id uuid NOT NULL,
  engineering_le_id text NOT NULL,
  operational_le_id text NOT NULL,
  engineering_site text NOT NULL DEFAULT '',
  operational_site text NOT NULL DEFAULT '',
  PRIMARY KEY (tenant_id, engineering_le_id, operational_le_id, engineering_site)
);
CREATE TABLE IF NOT EXISTS release_session (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  number text NOT NULL,
  created_by text NOT NULL,
  source text NOT NULL CHECK (source IN ('manual', 'eco', 'job')),
  policy_id uuid NOT NULL,
  eco_id uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, number)
);
CREATE TABLE IF NOT EXISTS release_line (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  session_id uuid NOT NULL,
  product_id text NOT NULL,
  engineering_version text NOT NULL,
  legal_entity_id text NOT NULL,
  status text NOT NULL CHECK (status IN ('pending', 'sent', 'accepted', 'rejected', 'failed')),
  reason text NOT NULL DEFAULT '',
  accepted_by text NOT NULL DEFAULT '',
  retries integer NOT NULL DEFAULT 0,
  copied_bom_version_id uuid,
  copied_route_version_id uuid,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, session_id) REFERENCES release_session (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS "IX-ENG-10" ON release_line (tenant_id, status, legal_entity_id);
CREATE TABLE IF NOT EXISTS readiness_policy (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL,
  checks jsonb NOT NULL DEFAULT '[]',
  target_state text NOT NULL DEFAULT 'Active',
  continuous boolean NOT NULL DEFAULT false,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code)
);
CREATE TABLE IF NOT EXISTS released_product (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  product_id text NOT NULL,
  legal_entity_id text NOT NULL,
  engineering_version text NOT NULL DEFAULT '',
  session_id uuid,
  state text NOT NULL DEFAULT 'readiness_pending',
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, product_id, legal_entity_id)
);
CREATE TABLE IF NOT EXISTS readiness_check_result (
  tenant_id uuid NOT NULL,
  released_product_id uuid NOT NULL,
  check_key text NOT NULL,
  kind text NOT NULL,
  status text NOT NULL CHECK (status IN ('pending', 'passed', 'failed', 'not_applicable')),
  signed_by text NOT NULL DEFAULT '',
  evaluated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, released_product_id, check_key),
  FOREIGN KEY (tenant_id, released_product_id) REFERENCES released_product (tenant_id, id)
);
CREATE TABLE IF NOT EXISTS document_type (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL,
  name text NOT NULL,
  change_controlled boolean NOT NULL DEFAULT false,
  signature_on_release boolean NOT NULL DEFAULT false,
  independent_review boolean NOT NULL DEFAULT false,
  signature_meaning text NOT NULL DEFAULT 'Approved for release',
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code)
);
CREATE TABLE IF NOT EXISTS document (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  type_code text NOT NULL,
  number text NOT NULL,
  part text NOT NULL DEFAULT '000',
  version text NOT NULL DEFAULT '01',
  description text NOT NULL DEFAULT '',
  status text NOT NULL CHECK (status IN ('in_work', 'in_review', 'released', 'obsolete', 'cancelled')),
  last_editor text NOT NULL DEFAULT '',
  eco_id uuid,
  signed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS "IX-ENG-11" ON document (tenant_id, number, version);
CREATE TABLE IF NOT EXISTS document_original (
  tenant_id uuid NOT NULL,
  document_id uuid NOT NULL,
  storage_key text NOT NULL,
  sha256 text NOT NULL,
  size_bytes bigint NOT NULL DEFAULT 0,
  PRIMARY KEY (tenant_id, document_id, storage_key),
  FOREIGN KEY (tenant_id, document_id) REFERENCES document (tenant_id, id)
);
CREATE TABLE IF NOT EXISTS document_link (
  tenant_id uuid NOT NULL,
  document_id uuid NOT NULL,
  target_type text NOT NULL,
  target_id text NOT NULL,
  PRIMARY KEY (tenant_id, document_id, target_type, target_id),
  FOREIGN KEY (tenant_id, document_id) REFERENCES document (tenant_id, id)
);
CREATE TABLE IF NOT EXISTS document_structure (
  tenant_id uuid NOT NULL,
  parent_id uuid NOT NULL,
  child_id uuid NOT NULL,
  position integer NOT NULL DEFAULT 0,
  PRIMARY KEY (tenant_id, parent_id, child_id),
  FOREIGN KEY (tenant_id, parent_id) REFERENCES document (tenant_id, id),
  FOREIGN KEY (tenant_id, child_id) REFERENCES document (tenant_id, id)
);
CREATE TABLE IF NOT EXISTS document_signature (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  document_id uuid NOT NULL,
  document_number text NOT NULL,
  document_version text NOT NULL,
  transition text NOT NULL,
  signer_id text NOT NULL,
  printed_name text NOT NULL,
  meaning text NOT NULL,
  signed_at timestamptz NOT NULL,
  originals_hash char(64) NOT NULL,
  method text NOT NULL DEFAULT '',
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, document_id) REFERENCES document (tenant_id, id)
);
CREATE OR REPLACE FUNCTION document_signature_immutable() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'document_signature is append-only';
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS document_signature_no_mutation ON document_signature;
CREATE TRIGGER document_signature_no_mutation
  BEFORE UPDATE OR DELETE ON document_signature
  FOR EACH ROW EXECUTE PROCEDURE document_signature_immutable();
CREATE TABLE IF NOT EXISTS eco_document_replacement (
  tenant_id uuid NOT NULL,
  eco_id uuid NOT NULL,
  old_document_id uuid NOT NULL,
  new_document_id uuid NOT NULL,
  PRIMARY KEY (tenant_id, eco_id, new_document_id),
  FOREIGN KEY (tenant_id, eco_id) REFERENCES eco (tenant_id, id)
);
CREATE TABLE IF NOT EXISTS product_identifier (
  tenant_id uuid NOT NULL,
  id_type text NOT NULL,
  value text NOT NULL,
  product_id text NOT NULL,
  PRIMARY KEY (tenant_id, id_type, value)
);
CREATE TABLE IF NOT EXISTS ebom_import (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  source_code text NOT NULL,
  part_number text NOT NULL,
  revision text NOT NULL,
  status text NOT NULL CHECK (status IN ('draft', 'released')),
  payload jsonb NOT NULL,
  bom_version_id uuid,
  ecr_id uuid,
  added integer NOT NULL DEFAULT 0,
  removed integer NOT NULL DEFAULT 0,
  changed integer NOT NULL DEFAULT 0,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, source_code, part_number, revision)
);
CREATE TABLE IF NOT EXISTS ebom_exception (
  tenant_id uuid NOT NULL,
  import_id uuid NOT NULL,
  part_number text NOT NULL,
  code text NOT NULL,
  PRIMARY KEY (tenant_id, import_id, part_number),
  FOREIGN KEY (tenant_id, import_id) REFERENCES ebom_import (tenant_id, id)
);
DO $$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY[
    'engineering_organization', 'version_nomenclature', 'engineering_category', 'engineering_version',
    'release_policy', 'site_mapping', 'release_session', 'release_line', 'readiness_policy',
    'released_product', 'readiness_check_result', 'document_type', 'document', 'document_original',
    'document_link', 'document_structure', 'document_signature', 'eco_document_replacement',
    'product_identifier', 'ebom_import', 'ebom_exception'
  ] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)', t || '_tenant_access', t);
  END LOOP;
END
$$;
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON engineering_organization, version_nomenclature, engineering_category, engineering_version, release_policy, site_mapping, release_session, release_line, readiness_policy, released_product, readiness_check_result, document_type, document, document_original, document_link, document_structure, eco_document_replacement, product_identifier, ebom_import, ebom_exception TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON document_signature TO passage_app';
  END IF;
END
$$;
