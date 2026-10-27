CREATE EXTENSION IF NOT EXISTS btree_gist;

-- Worker: one wave-7 person party, personnel number from sequence WRK (caller
-- supplied in this slice; the wave-5 sequence client is a later slice).
CREATE TABLE IF NOT EXISTS worker (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  personnel_number text NOT NULL,
  worker_type text NOT NULL CHECK (worker_type IN ('EMPLOYEE', 'CONTRACTOR')),
  party_person_id uuid NOT NULL,
  legal_name text NOT NULL,
  preferred_name text,
  user_id uuid,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, personnel_number),
  UNIQUE (tenant_id, idempotency_key)
);

-- PII vault (FR-42.1): column-level AES-256-GCM sealed values. Readable only
-- through the domain Sealer; never returned unmasked without HcmPIIRead.
CREATE TABLE IF NOT EXISTS worker_pii (
  tenant_id uuid NOT NULL,
  worker_id uuid NOT NULL,
  national_id_sealed bytea NOT NULL,
  bank_account_sealed bytea,
  key_version int NOT NULL DEFAULT 1,
  PRIMARY KEY (tenant_id, worker_id),
  FOREIGN KEY (tenant_id, worker_id) REFERENCES worker (tenant_id, id)
);

-- Employment per legal entity, [valid_from, valid_to) (BR-42.1, BR-42.14).
CREATE TABLE IF NOT EXISTS employment (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  worker_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  employment_type text NOT NULL CHECK (employment_type IN ('FULL_TIME', 'PART_TIME', 'TEMP', 'CONTRACTOR')),
  assignment_kind text NOT NULL DEFAULT 'PRIMARY' CHECK (assignment_kind IN ('PRIMARY', 'CONCURRENT', 'GLOBAL_HOST')),
  home_legal_entity_id uuid,
  planned_end_date date,
  hire_date date NOT NULL,
  termination_date date,
  valid_from timestamptz NOT NULL,
  valid_to timestamptz,
  validity tstzrange GENERATED ALWAYS AS (tstzrange(valid_from, valid_to, '[)')) STORED,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, worker_id) REFERENCES worker (tenant_id, id),
  CHECK (valid_to IS NULL OR valid_to > valid_from),
  CHECK (termination_date IS NULL OR termination_date >= hire_date),
  CHECK (planned_end_date IS NULL OR planned_end_date >= hire_date),
  CHECK (assignment_kind <> 'GLOBAL_HOST' OR (home_legal_entity_id IS NOT NULL AND home_legal_entity_id <> legal_entity_id AND planned_end_date IS NOT NULL)),
  CONSTRAINT employment_primary_no_overlap EXCLUDE USING gist (
    tenant_id WITH =, worker_id WITH =, legal_entity_id WITH =, validity WITH &&
  ) WHERE (assignment_kind = 'PRIMARY')
);
CREATE INDEX IF NOT EXISTS employment_worker ON employment (tenant_id, worker_id);

-- Position with FTE and department; overfill is checked in the write path
-- because the FTE limit spans several assignment rows (FR-42.3).
CREATE TABLE IF NOT EXISTS hr_position (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  position_number text NOT NULL,
  job_code text NOT NULL,
  department_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  fte numeric(4, 2) NOT NULL CHECK (fte >= 0.05 AND fte <= 2.00),
  allow_overfill boolean NOT NULL DEFAULT false,
  valid_from timestamptz NOT NULL,
  valid_to timestamptz,
  validity tstzrange GENERATED ALWAYS AS (tstzrange(valid_from, valid_to, '[)')) STORED,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, position_number),
  UNIQUE (tenant_id, idempotency_key),
  CHECK (valid_to IS NULL OR valid_to > valid_from)
);

-- Reports-to edges. One parent per child at an instant; cycles are refused
-- on write (BR-42.7).
CREATE TABLE IF NOT EXISTS position_hierarchy (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  child_position_id uuid NOT NULL,
  parent_position_id uuid NOT NULL,
  valid_from timestamptz NOT NULL,
  valid_to timestamptz,
  validity tstzrange GENERATED ALWAYS AS (tstzrange(valid_from, valid_to, '[)')) STORED,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, child_position_id) REFERENCES hr_position (tenant_id, id),
  FOREIGN KEY (tenant_id, parent_position_id) REFERENCES hr_position (tenant_id, id),
  CHECK (child_position_id <> parent_position_id),
  CHECK (valid_to IS NULL OR valid_to > valid_from),
  CONSTRAINT position_hierarchy_one_parent EXCLUDE USING gist (
    tenant_id WITH =, child_position_id WITH =, validity WITH &&
  )
);
CREATE INDEX IF NOT EXISTS position_hierarchy_parent ON position_hierarchy (tenant_id, parent_position_id);

CREATE TABLE IF NOT EXISTS position_assignment (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  position_id uuid NOT NULL,
  worker_id uuid NOT NULL,
  employment_id uuid NOT NULL,
  fte_share numeric(4, 2) NOT NULL CHECK (fte_share > 0 AND fte_share <= 2.00),
  is_primary boolean NOT NULL DEFAULT false,
  valid_from timestamptz NOT NULL,
  valid_to timestamptz,
  validity tstzrange GENERATED ALWAYS AS (tstzrange(valid_from, valid_to, '[)')) STORED,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, position_id) REFERENCES hr_position (tenant_id, id),
  FOREIGN KEY (tenant_id, worker_id) REFERENCES worker (tenant_id, id),
  FOREIGN KEY (tenant_id, employment_id) REFERENCES employment (tenant_id, id),
  CHECK (valid_to IS NULL OR valid_to > valid_from),
  CONSTRAINT position_assignment_primary_once EXCLUDE USING gist (
    tenant_id WITH =, worker_id WITH =, validity WITH &&
  ) WHERE (is_primary)
);
CREATE INDEX IF NOT EXISTS position_assignment_position ON position_assignment (tenant_id, position_id, validity);
CREATE INDEX IF NOT EXISTS position_assignment_worker ON position_assignment (tenant_id, worker_id);

-- PII access audit (kernel audit_event). Holds who, when, which fields and the
-- reason; never the P3 values themselves.
CREATE TABLE IF NOT EXISTS audit_event (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  occurred_at timestamptz NOT NULL DEFAULT now(),
  actor text NOT NULL,
  action text NOT NULL CHECK (action IN ('p3_read', 'p3_reveal', 'p3_change', 'export', 'break_glass')),
  entity text NOT NULL,
  entity_id uuid NOT NULL,
  fields text[] NOT NULL DEFAULT '{}',
  reason text,
  request_id text,
  PRIMARY KEY (tenant_id, id)
);

-- Transactional outbox, relayed by Debezium (kernel dbx.OutboxDDL), tenant-scoped like every table.
CREATE TABLE IF NOT EXISTS outbox (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL,
  aggregate_type text NOT NULL,
  aggregate_id text NOT NULL,
  type text NOT NULL,
  payload jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE worker ENABLE ROW LEVEL SECURITY;
ALTER TABLE worker FORCE ROW LEVEL SECURITY;
CREATE POLICY worker_tenant_access ON worker AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE worker_pii ENABLE ROW LEVEL SECURITY;
ALTER TABLE worker_pii FORCE ROW LEVEL SECURITY;
CREATE POLICY worker_pii_tenant_access ON worker_pii AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE employment ENABLE ROW LEVEL SECURITY;
ALTER TABLE employment FORCE ROW LEVEL SECURITY;
CREATE POLICY employment_tenant_access ON employment AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY employment_legal_entity_scope ON employment AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE hr_position ENABLE ROW LEVEL SECURITY;
ALTER TABLE hr_position FORCE ROW LEVEL SECURITY;
CREATE POLICY hr_position_tenant_access ON hr_position AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY hr_position_legal_entity_scope ON hr_position AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE position_hierarchy ENABLE ROW LEVEL SECURITY;
ALTER TABLE position_hierarchy FORCE ROW LEVEL SECURITY;
CREATE POLICY position_hierarchy_tenant_access ON position_hierarchy AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE position_assignment ENABLE ROW LEVEL SECURITY;
ALTER TABLE position_assignment FORCE ROW LEVEL SECURITY;
CREATE POLICY position_assignment_tenant_access ON position_assignment AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE audit_event ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_event FORCE ROW LEVEL SECURITY;
CREATE POLICY audit_event_tenant_access ON audit_event AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox FORCE ROW LEVEL SECURITY;
CREATE POLICY outbox_tenant_access ON outbox AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
