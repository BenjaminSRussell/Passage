-- Integration hub: the API and event catalog (FR-35.8, §6.35.10), the registry the
-- developer portal browses. One row per published contract version: an OpenAPI
-- document (kind api) or an AsyncAPI document (kind event). The document of a
-- published version never changes; a new version is a new row, a lifecycle moves
-- active -> deprecated (with a sunset date at least api_deprecation_months away)
-- -> retired, and nothing is deleted, so passage_app gets no DELETE.
-- The document is stored as json, not jsonb, so it is returned exactly as published
-- (jsonb would reorder the keys, and with them the order of paths and messages);
-- spec_sha256 is the digest of that text, used to tell an idempotent repeat from a
-- different publish under the same key.
-- tenant_id carries ENABLE + FORCE row-level security and the tenant policy.

CREATE TABLE IF NOT EXISTS hub_catalog_entry (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  kind text NOT NULL CHECK (kind IN ('api', 'event')),
  name text NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9._-]{1,99}$'),
  spec_version text NOT NULL CHECK (spec_version ~ '^[0-9]{1,6}\.[0-9]{1,6}\.[0-9]{1,6}$'),
  title text NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
  summary text NOT NULL DEFAULT '' CHECK (length(summary) <= 2000),
  owner_service text NOT NULL CHECK (owner_service ~ '^[a-z0-9][a-z0-9._-]{1,63}$'),
  spec_format text NOT NULL CHECK ((kind = 'api' AND spec_format = 'openapi') OR (kind = 'event' AND spec_format = 'asyncapi')),
  spec json NOT NULL,
  spec_bytes integer NOT NULL CHECK (spec_bytes BETWEEN 2 AND 2097152),
  spec_sha256 text NOT NULL CHECK (spec_sha256 ~ '^[0-9a-f]{64}$'),
  -- Operations of an OpenAPI document or messages of an AsyncAPI document, derived on publish.
  operations jsonb NOT NULL DEFAULT '[]'::jsonb,
  operation_count integer NOT NULL DEFAULT 0 CHECK (operation_count >= 0),
  lifecycle text NOT NULL DEFAULT 'active' CHECK (lifecycle IN ('active', 'deprecated', 'retired')),
  deprecated_at timestamptz,
  sunset_at timestamptz,
  replaced_by uuid,
  retired_at timestamptz,
  version integer NOT NULL DEFAULT 1,
  idempotency_key text,
  created_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, kind, name, spec_version),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, replaced_by) REFERENCES hub_catalog_entry (tenant_id, id),
  CHECK (lifecycle = 'active' OR (deprecated_at IS NOT NULL AND sunset_at IS NOT NULL AND sunset_at > deprecated_at)),
  CHECK ((lifecycle = 'retired') = (retired_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS hub_catalog_entry_list ON hub_catalog_entry (tenant_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS hub_catalog_entry_name ON hub_catalog_entry (tenant_id, kind, name);
ALTER TABLE hub_catalog_entry ENABLE ROW LEVEL SECURITY;
ALTER TABLE hub_catalog_entry FORCE ROW LEVEL SECURITY;
CREATE POLICY hub_catalog_entry_tenant_access ON hub_catalog_entry AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON hub_catalog_entry TO passage_app';
  END IF;
END
$$;
