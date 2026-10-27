-- Wave 33 slice 2: product compliance (FR-33.13), stability studies (FR-33.16)
-- and the read index behind SPC charts (FR-33.11).

-- Compliance lists are versioned: a new version is a new immutable row, numbered
-- per code by the store; the highest version is the one evaluated (FR-33.13).
CREATE TABLE IF NOT EXISTS quality_compliance_list (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{1,31}$'),
  version int NOT NULL CHECK (version >= 1),
  name text NOT NULL DEFAULT '' CHECK (char_length(name) <= 120),
  kind text NOT NULL CHECK (kind IN ('RESTRICTED','REPORTING')),
  block_mode text NOT NULL CHECK (block_mode IN ('BLOCK','WARN')),
  regions jsonb NOT NULL CHECK (jsonb_typeof(regions) = 'array'),
  limits jsonb NOT NULL CHECK (jsonb_typeof(limits) = 'array'),
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code, version),
  UNIQUE (tenant_id, idempotency_key)
);
ALTER TABLE quality_compliance_list ENABLE ROW LEVEL SECURITY;
ALTER TABLE quality_compliance_list FORCE ROW LEVEL SECURITY;
CREATE POLICY quality_compliance_list_tenant_access ON quality_compliance_list AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Item compositions are immutable versions; the highest version is current (BR-33.11).
CREATE TABLE IF NOT EXISTS quality_item_composition (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  item_id uuid NOT NULL,
  version int NOT NULL CHECK (version >= 1),
  direct jsonb NOT NULL CHECK (jsonb_typeof(direct) = 'object'),
  parts jsonb NOT NULL CHECK (jsonb_typeof(parts) = 'array'),
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, item_id, version),
  UNIQUE (tenant_id, idempotency_key)
);
ALTER TABLE quality_item_composition ENABLE ROW LEVEL SECURITY;
ALTER TABLE quality_item_composition FORCE ROW LEVEL SECURITY;
CREATE POLICY quality_item_composition_tenant_access ON quality_item_composition AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Evaluation results are versioned per (item, list version, region): a
-- re-evaluation appends a version only when the outcome changed (BR-33.11).
CREATE TABLE IF NOT EXISTS quality_compliance_result (
  tenant_id uuid NOT NULL,
  item_id uuid NOT NULL,
  list_code text NOT NULL,
  list_version int NOT NULL,
  region text NOT NULL,
  version int NOT NULL CHECK (version >= 1),
  composition_version int NOT NULL,
  restricted boolean NOT NULL,
  code text NOT NULL,
  severity text NOT NULL,
  hits jsonb NOT NULL CHECK (jsonb_typeof(hits) = 'array'),
  evaluated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, item_id, list_code, list_version, region, version)
);
ALTER TABLE quality_compliance_result ENABLE ROW LEVEL SECURITY;
ALTER TABLE quality_compliance_result FORCE ROW LEVEL SECURITY;
CREATE POLICY quality_compliance_result_tenant_access ON quality_compliance_result AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Stability studies and their generated pull points (FR-33.16, BR-33.14).
CREATE TABLE IF NOT EXISTS quality_stability_study (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  item_id uuid NOT NULL,
  batch_id text NOT NULL CHECK (char_length(batch_id) BETWEEN 1 AND 60),
  protocol text NOT NULL CHECK (char_length(protocol) BETWEEN 1 AND 120),
  condition text NOT NULL CHECK (char_length(condition) BETWEEN 1 AND 60),
  start_date date NOT NULL,
  test_group_id uuid NOT NULL,
  lead_days int NOT NULL CHECK (lead_days BETWEEN 0 AND 30),
  window_days int NOT NULL CHECK (window_days BETWEEN 0 AND 14),
  status text NOT NULL CHECK (status IN ('PLANNED','ACTIVE','COMPLETED','TERMINATED')),
  version int NOT NULL DEFAULT 1,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, test_group_id) REFERENCES quality_test_group (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS quality_stability_study_created ON quality_stability_study (tenant_id, created_at DESC, id);
ALTER TABLE quality_stability_study ENABLE ROW LEVEL SECURITY;
ALTER TABLE quality_stability_study FORCE ROW LEVEL SECURITY;
CREATE POLICY quality_stability_study_tenant_access ON quality_stability_study AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS quality_pull_point (
  tenant_id uuid NOT NULL,
  study_id uuid NOT NULL,
  months int NOT NULL CHECK (months BETWEEN 0 AND 120),
  pull_date date NOT NULL,
  order_date date NOT NULL,
  test_group_id uuid NOT NULL,
  status text NOT NULL DEFAULT 'PLANNED' CHECK (status IN ('PLANNED','ORDERED','PULLED','MISSED')),
  PRIMARY KEY (tenant_id, study_id, months),
  FOREIGN KEY (tenant_id, study_id) REFERENCES quality_stability_study (tenant_id, id),
  FOREIGN KEY (tenant_id, test_group_id) REFERENCES quality_test_group (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS quality_pull_point_due ON quality_pull_point (tenant_id, status, order_date);
ALTER TABLE quality_pull_point ENABLE ROW LEVEL SECURITY;
ALTER TABLE quality_pull_point FORCE ROW LEVEL SECURITY;
CREATE POLICY quality_pull_point_tenant_access ON quality_pull_point AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- SPC reads the first-attempt results of one test on one item's orders (FR-33.11).
CREATE INDEX IF NOT EXISTS quality_result_test ON quality_result (tenant_id, test_id, retest_no, quality_order_id);
CREATE INDEX IF NOT EXISTS quality_order_item ON quality_order (tenant_id, item_id);
