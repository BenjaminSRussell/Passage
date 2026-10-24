CREATE TABLE metadata_entity_version (
  tenant_id uuid NOT NULL,
  entity_name text NOT NULL,
  version integer NOT NULL,
  definition jsonb NOT NULL,
  changes jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  created_by uuid NOT NULL,
  PRIMARY KEY (tenant_id, entity_name, version)
);
CREATE INDEX metadata_entity_version_current
  ON metadata_entity_version (tenant_id, entity_name, version DESC);
ALTER TABLE metadata_entity_version ENABLE ROW LEVEL SECURITY;
ALTER TABLE metadata_entity_version FORCE ROW LEVEL SECURITY;
CREATE POLICY metadata_entity_version_tenant_access ON metadata_entity_version AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE metadata_extension (
  tenant_id uuid NOT NULL,
  name text NOT NULL,
  entity_name text NOT NULL,
  definition jsonb NOT NULL,
  applied_version integer NOT NULL,
  applied_at timestamptz NOT NULL DEFAULT now(),
  applied_by uuid NOT NULL,
  PRIMARY KEY (tenant_id, name),
  UNIQUE (tenant_id, entity_name, applied_version)
);
ALTER TABLE metadata_extension ENABLE ROW LEVEL SECURITY;
ALTER TABLE metadata_extension FORCE ROW LEVEL SECURITY;
CREATE POLICY metadata_extension_tenant_access ON metadata_extension AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE metadata_record (
  tenant_id uuid NOT NULL,
  entity_name text NOT NULL,
  id uuid NOT NULL,
  data jsonb NOT NULL,
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  created_by uuid NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  updated_by uuid NOT NULL,
  PRIMARY KEY (tenant_id, entity_name, id)
);
CREATE INDEX metadata_record_list ON metadata_record (tenant_id, entity_name, created_at DESC, id DESC);
ALTER TABLE metadata_record ENABLE ROW LEVEL SECURITY;
ALTER TABLE metadata_record FORCE ROW LEVEL SECURITY;
CREATE POLICY metadata_record_tenant_access ON metadata_record AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE metadata_decision_table (
  tenant_id uuid NOT NULL,
  name text NOT NULL,
  version integer NOT NULL,
  definition jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  created_by uuid NOT NULL,
  PRIMARY KEY (tenant_id, name, version)
);
ALTER TABLE metadata_decision_table ENABLE ROW LEVEL SECURITY;
ALTER TABLE metadata_decision_table FORCE ROW LEVEL SECURITY;
CREATE POLICY metadata_decision_table_tenant_access ON metadata_decision_table AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE metadata_usage_daily (
  tenant_id uuid NOT NULL,
  day date NOT NULL,
  legal_entity_id text NOT NULL DEFAULT '',
  role text NOT NULL DEFAULT '',
  entity_name text NOT NULL DEFAULT '',
  named_users bigint NOT NULL DEFAULT 0,
  principals bigint NOT NULL DEFAULT 0,
  api_requests bigint NOT NULL DEFAULT 0,
  rows_read bigint NOT NULL DEFAULT 0,
  rows_stored bigint NOT NULL DEFAULT 0,
  PRIMARY KEY (tenant_id, day, legal_entity_id, role, entity_name)
);
ALTER TABLE metadata_usage_daily ENABLE ROW LEVEL SECURITY;
ALTER TABLE metadata_usage_daily FORCE ROW LEVEL SECURITY;
CREATE POLICY metadata_usage_daily_tenant_access ON metadata_usage_daily AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
