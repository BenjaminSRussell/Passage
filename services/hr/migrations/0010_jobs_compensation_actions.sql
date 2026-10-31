CREATE TABLE IF NOT EXISTS hr_job (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  title text NOT NULL CHECK (length(title) BETWEEN 1 AND 120),
  job_type text NOT NULL CHECK (job_type ~ '^[A-Z0-9][A-Z0-9_-]{0,39}$'),
  job_function text CHECK (job_function IS NULL OR length(job_function) BETWEEN 1 AND 60),
  pay_currency char(3) CHECK (pay_currency IS NULL OR pay_currency ~ '^[A-Z]{3}$'),
  pay_min numeric(20, 6) CHECK (pay_min IS NULL OR pay_min >= 0),
  pay_max numeric(20, 6),
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code),
  UNIQUE (tenant_id, idempotency_key),
  CHECK ((pay_min IS NULL) = (pay_max IS NULL) AND (pay_min IS NULL) = (pay_currency IS NULL)),
  CHECK (pay_max IS NULL OR pay_max >= pay_min)
);

CREATE TABLE IF NOT EXISTS hr_compensation_plan (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
  pay_basis text NOT NULL CHECK (pay_basis IN ('ANNUAL', 'HOURLY')),
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code),
  UNIQUE (tenant_id, idempotency_key)
);

ALTER TABLE employment ADD COLUMN IF NOT EXISTS termination_reason text
  CHECK (termination_reason IS NULL OR termination_reason ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$');

CREATE TABLE IF NOT EXISTS hr_fixed_compensation (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  employment_id uuid NOT NULL,
  worker_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  plan_id uuid NOT NULL,
  amount numeric(20, 6) NOT NULL CHECK (amount >= 0),
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  valid_from timestamptz NOT NULL,
  valid_to timestamptz,
  validity tstzrange GENERATED ALWAYS AS (tstzrange(valid_from, valid_to, '[)')) STORED,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, employment_id) REFERENCES employment (tenant_id, id),
  FOREIGN KEY (tenant_id, worker_id) REFERENCES worker (tenant_id, id),
  FOREIGN KEY (tenant_id, plan_id) REFERENCES hr_compensation_plan (tenant_id, id),
  CHECK (valid_to IS NULL OR valid_to > valid_from),
  CONSTRAINT hr_fixed_compensation_no_overlap EXCLUDE USING gist (
    tenant_id WITH =, employment_id WITH =, validity WITH &&
  )
);
CREATE INDEX IF NOT EXISTS hr_fixed_compensation_worker ON hr_fixed_compensation (tenant_id, worker_id);

CREATE TABLE IF NOT EXISTS hr_personnel_action (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  action_type text NOT NULL CHECK (action_type IN ('HIRE', 'TRANSFER', 'TERMINATE', 'COMP_CHANGE')),
  worker_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  employment_id uuid,
  effective_date date NOT NULL,
  status text NOT NULL CHECK (status IN ('PENDING', 'APPLIED', 'WITHDRAWN', 'FAILED')),
  payload jsonb NOT NULL,
  reason text CHECK (reason IS NULL OR length(reason) BETWEEN 1 AND 500),
  source_kind text CHECK (source_kind IS NULL OR source_kind IN ('OFFER', 'COMP_CYCLE')),
  source_id uuid,
  failure_code text,
  failure_detail text,
  submitted_by text NOT NULL,
  submitted_at timestamptz NOT NULL DEFAULT now(),
  applied_by text,
  applied_at timestamptz,
  withdrawn_by text,
  withdrawn_at timestamptz,
  idempotency_key text,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, worker_id) REFERENCES worker (tenant_id, id),
  FOREIGN KEY (tenant_id, employment_id) REFERENCES employment (tenant_id, id),
  CHECK ((source_kind IS NULL) = (source_id IS NULL)),
  CHECK ((status = 'APPLIED') = (applied_at IS NOT NULL)),
  CHECK ((status = 'WITHDRAWN') = (withdrawn_at IS NOT NULL)),
  CHECK ((status = 'FAILED') = (failure_code IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS hr_personnel_action_due ON hr_personnel_action (tenant_id, status, effective_date);
CREATE INDEX IF NOT EXISTS hr_personnel_action_worker ON hr_personnel_action (tenant_id, worker_id, effective_date);

ALTER TABLE hr_job ENABLE ROW LEVEL SECURITY;
ALTER TABLE hr_job FORCE ROW LEVEL SECURITY;
CREATE POLICY hr_job_tenant_access ON hr_job AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE hr_compensation_plan ENABLE ROW LEVEL SECURITY;
ALTER TABLE hr_compensation_plan FORCE ROW LEVEL SECURITY;
CREATE POLICY hr_compensation_plan_tenant_access ON hr_compensation_plan AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE hr_fixed_compensation ENABLE ROW LEVEL SECURITY;
ALTER TABLE hr_fixed_compensation FORCE ROW LEVEL SECURITY;
CREATE POLICY hr_fixed_compensation_tenant_access ON hr_fixed_compensation AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY hr_fixed_compensation_legal_entity_scope ON hr_fixed_compensation AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE hr_personnel_action ENABLE ROW LEVEL SECURITY;
ALTER TABLE hr_personnel_action FORCE ROW LEVEL SECURITY;
CREATE POLICY hr_personnel_action_tenant_access ON hr_personnel_action AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY hr_personnel_action_legal_entity_scope ON hr_personnel_action AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON hr_job, hr_compensation_plan TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON hr_fixed_compensation, hr_personnel_action TO passage_app';
    EXECUTE 'GRANT UPDATE ON employment, position_assignment TO passage_app';
  END IF;
END
$$;

DO $$
DECLARE
  schema_name text := current_schema();
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'hr_loan_gate' AND rolbypassrls AND NOT rolcanlogin) THEN
    RAISE EXCEPTION 'role hr_loan_gate must exist as NOLOGIN BYPASSRLS (create it in the database bootstrap before migrating)';
  END IF;
  EXECUTE format('GRANT USAGE ON SCHEMA %I TO hr_loan_gate', schema_name);
  EXECUTE format('GRANT SELECT ON employment TO hr_loan_gate');
  EXECUTE format($f$
    CREATE OR REPLACE FUNCTION hr_worker_has_other_employment(p_worker uuid, p_excluding uuid, p_at timestamptz) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, %I, pg_temp AS $body$
      SELECT EXISTS (SELECT 1 FROM employment
        WHERE tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid AND worker_id = p_worker AND id <> p_excluding
          AND (valid_to IS NULL OR valid_to > p_at))
    $body$
  $f$, schema_name);
  EXECUTE 'ALTER FUNCTION hr_worker_has_other_employment(uuid, uuid, timestamptz) OWNER TO hr_loan_gate';
  EXECUTE 'REVOKE ALL ON FUNCTION hr_worker_has_other_employment(uuid, uuid, timestamptz) FROM PUBLIC';
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT EXECUTE ON FUNCTION hr_worker_has_other_employment(uuid, uuid, timestamptz) TO passage_app';
  END IF;
END
$$;
