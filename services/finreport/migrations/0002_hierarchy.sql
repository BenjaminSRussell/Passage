-- Derived financial hierarchies (FR-15.13). A code has at most one draft (version 0,
-- editable, revision-checked) and any number of immutable published versions. The
-- membership of a published version is materialized at publish time.

CREATE TABLE IF NOT EXISTS report_hierarchy (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z][A-Z0-9-]{1,29}$'),
  version integer NOT NULL DEFAULT 0 CHECK (version >= 0),
  status text NOT NULL CHECK (status IN ('draft', 'published', 'retired')),
  revision integer NOT NULL DEFAULT 1,
  name text NOT NULL,
  subject text NOT NULL CHECK (subject IN ('main_account_category', 'main_account', 'dimension_value')),
  strict boolean NOT NULL,
  body jsonb NOT NULL,
  membership jsonb,
  owner_user_id text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  published_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code, version)
);
CREATE UNIQUE INDEX IF NOT EXISTS report_hierarchy_one_draft ON report_hierarchy (tenant_id, code) WHERE status = 'draft';
ALTER TABLE report_hierarchy ENABLE ROW LEVEL SECURITY;
ALTER TABLE report_hierarchy FORCE ROW LEVEL SECURITY;
CREATE POLICY report_hierarchy_tenant_access ON report_hierarchy AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
