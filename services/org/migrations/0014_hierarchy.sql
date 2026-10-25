CREATE TABLE IF NOT EXISTS hierarchy_type (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{1,19}$'),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  purposes text[] NOT NULL CHECK (cardinality(purposes) BETWEEN 1 AND 6 AND purposes <@ ARRAY['reporting', 'procurement', 'expenditure', 'security', 'centralized_payments', 'custom']::text[]),
  backdate_allowed boolean NOT NULL DEFAULT false,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  version integer NOT NULL DEFAULT 1,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code),
  UNIQUE (tenant_id, idempotency_key)
);
ALTER TABLE hierarchy_type ENABLE ROW LEVEL SECURITY;
ALTER TABLE hierarchy_type FORCE ROW LEVEL SECURITY;
CREATE POLICY hierarchy_type_tenant_access ON hierarchy_type AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS hierarchy_version (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  hierarchy_type_id uuid NOT NULL,
  version_no integer NOT NULL CHECK (version_no >= 1),
  status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published', 'superseded')),
  effective_from date,
  effective_to date,
  node_count integer NOT NULL DEFAULT 0,
  copied_from_version_id uuid,
  version integer NOT NULL DEFAULT 1,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, hierarchy_type_id, version_no),
  UNIQUE (tenant_id, idempotency_key),
  CHECK (status = 'draft' OR effective_from IS NOT NULL),
  CHECK (effective_to IS NULL OR effective_from < effective_to),
  FOREIGN KEY (tenant_id, hierarchy_type_id) REFERENCES hierarchy_type (tenant_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS hierarchy_version_one_draft ON hierarchy_version (tenant_id, hierarchy_type_id) WHERE status = 'draft';
ALTER TABLE hierarchy_version ENABLE ROW LEVEL SECURITY;
ALTER TABLE hierarchy_version FORCE ROW LEVEL SECURITY;
CREATE POLICY hierarchy_version_tenant_access ON hierarchy_version AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS hierarchy_node (
  tenant_id uuid NOT NULL,
  version_id uuid NOT NULL,
  id uuid NOT NULL,
  org_unit_kind text NOT NULL CHECK (org_unit_kind IN ('legal_entity', 'operating_unit')),
  org_unit_id uuid NOT NULL,
  parent_node_id uuid,
  sort_order integer NOT NULL DEFAULT 0 CHECK (sort_order >= 0),
  PRIMARY KEY (tenant_id, version_id, id),
  UNIQUE (tenant_id, version_id, org_unit_id),
  CHECK (parent_node_id IS NULL OR parent_node_id <> id),
  FOREIGN KEY (tenant_id, version_id) REFERENCES hierarchy_version (tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, version_id, parent_node_id) REFERENCES hierarchy_node (tenant_id, version_id, id) DEFERRABLE INITIALLY DEFERRED
);
ALTER TABLE hierarchy_node ENABLE ROW LEVEL SECURITY;
ALTER TABLE hierarchy_node FORCE ROW LEVEL SECURITY;
CREATE POLICY hierarchy_node_tenant_access ON hierarchy_node AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
