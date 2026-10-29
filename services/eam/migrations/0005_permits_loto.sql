-- Wave 44, slice 3: permits to work and lockout/tagout (FR-44.19, BR-44.11).
-- Permit types, job requirements, isolation points, permits with an append-only
-- transition log, and personal LOTO locks.

CREATE SEQUENCE IF NOT EXISTS permit_number_seq;
CREATE SEQUENCE IF NOT EXISTS lock_number_seq;

CREATE TABLE IF NOT EXISTS permit_type (
  tenant_id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9_]{2,20}$'),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  approver_role text NOT NULL CHECK (char_length(approver_role) BETWEEN 1 AND 60),
  max_validity_hours integer NOT NULL DEFAULT 12 CHECK (max_validity_hours BETWEEN 1 AND 168),
  requires_isolation boolean NOT NULL DEFAULT false,
  requires_gas_test boolean NOT NULL DEFAULT false,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, code)
);

CREATE TABLE IF NOT EXISTS job_permit_requirement (
  tenant_id uuid NOT NULL,
  job_type text NOT NULL CHECK (job_type ~ '^[A-Z0-9_]{2,20}$'),
  permit_type_code text NOT NULL,
  PRIMARY KEY (tenant_id, job_type, permit_type_code),
  FOREIGN KEY (tenant_id, permit_type_code) REFERENCES permit_type (tenant_id, code)
);

CREATE TABLE IF NOT EXISTS isolation_point (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  asset_id uuid NOT NULL,
  energy_type text NOT NULL CHECK (energy_type IN ('ELECTRICAL', 'PNEUMATIC', 'HYDRAULIC', 'MECHANICAL', 'THERMAL', 'CHEMICAL')),
  location_text text NOT NULL CHECK (char_length(location_text) BETWEEN 1 AND 200),
  method text NOT NULL CHECK (char_length(method) BETWEEN 1 AND 200),
  verification_step text NOT NULL CHECK (char_length(verification_step) BETWEEN 1 AND 200),
  tag_code text NOT NULL CHECK (char_length(tag_code) BETWEEN 1 AND 40),
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, asset_id, tag_code),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, asset_id) REFERENCES asset (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS permit (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  number text NOT NULL,
  permit_type_code text NOT NULL,
  work_order_id uuid NOT NULL,
  asset_id uuid NOT NULL,
  status text NOT NULL DEFAULT 'REQUESTED'
    CHECK (status IN ('REQUESTED', 'APPROVED', 'ISSUED', 'SUSPENDED', 'CLOSED', 'CANCELED')),
  valid_from timestamptz NOT NULL,
  valid_to timestamptz NOT NULL,
  requested_by text NOT NULL,
  approved_by text,
  issued_by text,
  closed_by text,
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, number),
  UNIQUE (tenant_id, idempotency_key),
  CHECK (valid_to > valid_from),
  FOREIGN KEY (tenant_id, permit_type_code) REFERENCES permit_type (tenant_id, code),
  FOREIGN KEY (tenant_id, work_order_id) REFERENCES work_order (tenant_id, id),
  FOREIGN KEY (tenant_id, asset_id) REFERENCES asset (tenant_id, id)
);

CREATE INDEX IF NOT EXISTS ix_eam_permit_wo ON permit (tenant_id, work_order_id, permit_type_code, created_at);
-- The expiry job scans ISSUED permits by valid_to (8.44.20 eam-permit-expiry).
CREATE INDEX IF NOT EXISTS ix_eam_permit_status_valid ON permit (tenant_id, status, valid_to);
-- One permit of a type governs a work order at a time: a second one may be
-- requested only after the first is closed or canceled.
CREATE UNIQUE INDEX IF NOT EXISTS ux_eam_permit_active ON permit (tenant_id, work_order_id, permit_type_code)
  WHERE status NOT IN ('CLOSED', 'CANCELED');

-- Append-only audit of every permit transition (retained 5 years, BR-44.11).
CREATE TABLE IF NOT EXISTS permit_transition (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  permit_id uuid NOT NULL,
  from_status text NOT NULL,
  to_status text NOT NULL,
  actor text NOT NULL,
  at timestamptz NOT NULL DEFAULT now(),
  idempotency_key text,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, permit_id) REFERENCES permit (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS ix_eam_permit_transition ON permit_transition (tenant_id, permit_id, at);

CREATE TABLE IF NOT EXISTS loto_lock (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  permit_id uuid NOT NULL,
  isolation_point_id uuid NOT NULL,
  technician_id text NOT NULL,
  lock_number text NOT NULL,
  state text NOT NULL CHECK (state IN ('APPLIED', 'VERIFIED', 'REMOVED')),
  applied_at timestamptz NOT NULL DEFAULT now(),
  verified_at timestamptz,
  removed_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, permit_id) REFERENCES permit (tenant_id, id),
  FOREIGN KEY (tenant_id, isolation_point_id) REFERENCES isolation_point (tenant_id, id)
);

-- One open lock per technician per isolation point and permit (BR-44.11).
CREATE UNIQUE INDEX IF NOT EXISTS ux_eam_loto_open ON loto_lock (tenant_id, permit_id, isolation_point_id, technician_id)
  WHERE state <> 'REMOVED';

-- Append-only log of every lock action (apply, verify = zero-energy check,
-- remove) with its actor; the Idempotency-Key of each action lives here.
CREATE TABLE IF NOT EXISTS loto_lock_action (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  lock_id uuid NOT NULL,
  action text NOT NULL CHECK (action IN ('APPLY', 'VERIFY', 'REMOVE')),
  actor text NOT NULL,
  at timestamptz NOT NULL,
  idempotency_key text,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, lock_id) REFERENCES loto_lock (tenant_id, id)
);

-- Row-level security: tenant_access on every new tenant table (same shape as 0001).
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['permit_type', 'job_permit_requirement', 'isolation_point', 'permit',
                           'permit_transition', 'loto_lock', 'loto_lock_action'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I_tenant_access ON %I AS PERMISSIVE
      USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)
      WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)', t, t);
  END LOOP;
END $$;

-- Application role privileges (same rules as 0004). permit_transition and
-- loto_lock_action are append-only audit logs (BR-44.11) and isolation points
-- are create-only. permit_type is upserted, permit and loto_lock are row-locked
-- and updated; a job type's requirements are replaced wholesale, so they also
-- get DELETE. Permit and lock numbers come from sequences.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON isolation_point, permit_transition, loto_lock_action TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON permit_type, permit, loto_lock TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON job_permit_requirement TO passage_app';
    EXECUTE 'GRANT USAGE ON SEQUENCE permit_number_seq, lock_number_seq TO passage_app';
  END IF;
END
$$;
