-- Field service, slice 4 (PSG-4504): generic case management (FR-45.12, BR-45.9,
-- BR-45.10). Case processes with ordered stages, the hierarchical case category
-- tree, cases in any non-service category, associations to any registry record
-- and the case log. Tenant RLS on every table; generic cases also carry
-- legal_entity_id and the legal-entity scope policy. Cases share the CS number
-- sequence of service cases (service_number_counter kind "case"), so a CS number
-- names one case across both tables.

CREATE TABLE IF NOT EXISTS service_case_process (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (char_length(code) BETWEEN 2 AND 40),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  -- [{"code": "INTAKE", "sequence": 1, "requiredActivities": ["Interview"]}, ...]
  stages jsonb NOT NULL CHECK (jsonb_typeof(stages) = 'array'),
  idempotency_key text,
  request_hash text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code),
  UNIQUE (tenant_id, idempotency_key)
);
ALTER TABLE service_case_process ENABLE ROW LEVEL SECURITY;
ALTER TABLE service_case_process FORCE ROW LEVEL SECURITY;
CREATE POLICY service_case_process_tenant_access ON service_case_process AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS service_case_category (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (char_length(code) BETWEEN 2 AND 40),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  category_type text NOT NULL CHECK (category_type IN ('SERVICE', 'CUSTOMER_SERVICE', 'HR', 'COLLECTIONS',
    'PRODUCT_CHANGE', 'AUDIT', 'PROJECT', 'VENDOR', 'INTERNAL')),
  parent_id uuid,
  process_id uuid,
  sla_profile_id uuid,
  default_priority smallint NOT NULL DEFAULT 3 CHECK (default_priority BETWEEN 1 AND 4),
  reopen_window_days integer NOT NULL DEFAULT 14 CHECK (reopen_window_days BETWEEN 0 AND 365),
  -- access_duty names the duty that, beside ownership, grants read and write on
  -- the category's cases (BR-45.10); empty leaves them to every CaseMaintain holder.
  access_duty text NOT NULL DEFAULT '' CHECK (char_length(access_duty) <= 100),
  resolution_codes jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(resolution_codes) = 'array'),
  idempotency_key text,
  request_hash text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, parent_id) REFERENCES service_case_category (tenant_id, id),
  FOREIGN KEY (tenant_id, process_id) REFERENCES service_case_process (tenant_id, id),
  FOREIGN KEY (tenant_id, sla_profile_id) REFERENCES service_sla_profile (tenant_id, id)
);
ALTER TABLE service_case_category ENABLE ROW LEVEL SECURITY;
ALTER TABLE service_case_category FORCE ROW LEVEL SECURITY;
CREATE POLICY service_case_category_tenant_access ON service_case_category AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS service_generic_case (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  number text NOT NULL,
  category_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  customer_party_id uuid,
  owner_id text NOT NULL DEFAULT '',
  subject text NOT NULL CHECK (char_length(subject) BETWEEN 1 AND 200),
  description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 4000),
  priority smallint NOT NULL CHECK (priority BETWEEN 1 AND 4),
  stage_code text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'RESOLVED', 'CLOSED', 'CANCELED')),
  resolution_code text,
  resolution_text text NOT NULL DEFAULT '' CHECK (char_length(resolution_text) <= 4000),
  -- The category's SLA profile at open (FR-45.12 "per-category SLA profile").
  sla_profile_id uuid,
  sla_response_due timestamptz,
  sla_resolution_due timestamptz,
  opened_at timestamptz NOT NULL,
  resolved_at timestamptz,
  closed_at timestamptz,
  reopened_from uuid,
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  idempotency_key text,
  request_hash text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, number),
  UNIQUE (tenant_id, idempotency_key),
  CHECK ((status = 'RESOLVED' OR status = 'CLOSED') = (resolution_code IS NOT NULL)),
  FOREIGN KEY (tenant_id, category_id) REFERENCES service_case_category (tenant_id, id),
  FOREIGN KEY (tenant_id, sla_profile_id) REFERENCES service_sla_profile (tenant_id, id),
  FOREIGN KEY (tenant_id, reopened_from) REFERENCES service_generic_case (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS service_generic_case_recent ON service_generic_case (tenant_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS service_generic_case_category ON service_generic_case (tenant_id, category_id, status, stage_code);
ALTER TABLE service_generic_case ENABLE ROW LEVEL SECURITY;
ALTER TABLE service_generic_case FORCE ROW LEVEL SECURITY;
CREATE POLICY service_generic_case_tenant_access ON service_generic_case AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY service_generic_case_legal_entity_scope ON service_generic_case AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS service_case_association (
  tenant_id uuid NOT NULL,
  case_id uuid NOT NULL,
  entity_type text NOT NULL CHECK (char_length(entity_type) BETWEEN 1 AND 40),
  entity_id uuid NOT NULL,
  role text NOT NULL DEFAULT 'RELATED' CHECK (role IN ('SUBJECT', 'RELATED')),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, case_id, entity_type, entity_id),
  FOREIGN KEY (tenant_id, case_id) REFERENCES service_generic_case (tenant_id, id)
);
-- The case list filters by an associated record (PRD 8.45.14 "case_list").
CREATE INDEX IF NOT EXISTS service_case_association_entity ON service_case_association (tenant_id, entity_type, entity_id);
ALTER TABLE service_case_association ENABLE ROW LEVEL SECURITY;
ALTER TABLE service_case_association FORCE ROW LEVEL SECURITY;
CREATE POLICY service_case_association_tenant_access ON service_case_association AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS service_case_log (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  case_id uuid NOT NULL,
  at timestamptz NOT NULL,
  actor text NOT NULL DEFAULT '',
  kind text NOT NULL CHECK (kind IN ('CREATED', 'STAGE', 'STATUS', 'ASSOCIATION', 'REOPENED')),
  text text NOT NULL DEFAULT '' CHECK (char_length(text) <= 1000),
  seq bigint GENERATED ALWAYS AS IDENTITY,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, case_id) REFERENCES service_generic_case (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS service_case_log_case ON service_case_log (tenant_id, case_id, seq);
ALTER TABLE service_case_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE service_case_log FORCE ROW LEVEL SECURITY;
CREATE POLICY service_case_log_tenant_access ON service_case_log AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Application role privileges, in the style of 0002. Processes, categories and the
-- case log are append-only. The store updates service_generic_case (stage, status,
-- reopen, association version bump), locking it FOR UPDATE, and upserts
-- service_case_association (a repeated association changes the role). Nothing is
-- deleted. Skipped when the role does not exist (developer databases without the
-- role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT ON service_case_process, service_case_category, service_case_log TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON service_generic_case, service_case_association TO passage_app';
  END IF;
END
$$;
