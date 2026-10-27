-- Benefit plans (FR-42.8). The open enrollment window is the inclusive date
-- range [enrollment_start, enrollment_end]; eligibility_rule is the CEL subset
-- of BR-42.6, compiled and checked by the API before it is stored. Rates are
-- money: numeric(20,6) with the plan's currency, never float (CONVENTIONS).
CREATE TABLE IF NOT EXISTS benefit_plan (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  plan_type text NOT NULL CHECK (plan_type IN ('MEDICAL', 'DENTAL', 'VISION', 'LIFE', 'RETIREMENT_401K', 'HSA', 'FSA', 'OTHER')),
  provider_party_id uuid,
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  eligibility_rule text NOT NULL CHECK (length(eligibility_rule) BETWEEN 1 AND 2000),
  enrollment_start date NOT NULL,
  enrollment_end date NOT NULL,
  waiting_days int NOT NULL DEFAULT 0 CHECK (waiting_days BETWEEN 0 AND 365),
  pre_tax boolean NOT NULL DEFAULT false,
  payroll_deduction_code text CHECK (payroll_deduction_code IS NULL OR length(payroll_deduction_code) BETWEEN 1 AND 20),
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code),
  UNIQUE (tenant_id, idempotency_key),
  CHECK (enrollment_end >= enrollment_start)
);

-- An option is a coverage level of a plan with its employee and employer rates.
CREATE TABLE IF NOT EXISTS benefit_plan_option (
  tenant_id uuid NOT NULL,
  plan_id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  coverage_level text NOT NULL CHECK (length(coverage_level) BETWEEN 1 AND 40),
  employee_rate numeric(20, 6) NOT NULL CHECK (employee_rate >= 0),
  employer_rate numeric(20, 6) NOT NULL CHECK (employer_rate >= 0),
  rate_basis text NOT NULL CHECK (rate_basis IN ('PER_PAY_PERIOD', 'MONTHLY')),
  PRIMARY KEY (tenant_id, plan_id, code),
  FOREIGN KEY (tenant_id, plan_id) REFERENCES benefit_plan (tenant_id, id)
);

-- A life event opens an enrollment window of 30 days on or after event_date
-- outside open enrollment (8.42.5, 8.42.13 "Benefit life-event window").
CREATE TABLE IF NOT EXISTS benefit_life_event (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  worker_id uuid NOT NULL,
  kind text NOT NULL CHECK (kind IN ('MARRIAGE', 'BIRTH', 'ADOPTION', 'DIVORCE', 'LOSS_OF_COVERAGE', 'OTHER')),
  event_date date NOT NULL,
  recorded_by text,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, worker_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, worker_id) REFERENCES worker (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS benefit_life_event_worker ON benefit_life_event (tenant_id, worker_id, event_date);

-- Enrollment of a worker in one option of a plan over [valid_from, valid_to)
-- (BR-42.6). The exclusion constraint keeps at most one enrollment per worker
-- and plan at any instant; a change of option ends one and starts another.
-- The life event must be the same worker's (composite foreign key).
CREATE TABLE IF NOT EXISTS benefit_enrollment (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  worker_id uuid NOT NULL,
  plan_id uuid NOT NULL,
  option_code text NOT NULL,
  valid_from timestamptz NOT NULL,
  valid_to timestamptz,
  validity tstzrange GENERATED ALWAYS AS (tstzrange(valid_from, valid_to, '[)')) STORED,
  life_event_id uuid,
  dependent_count int NOT NULL DEFAULT 0 CHECK (dependent_count BETWEEN 0 AND 20),
  end_reason text CHECK (end_reason IN ('TERMINATION', 'LIFE_EVENT', 'PLAN_END')),
  cobra_qualifying_event boolean NOT NULL DEFAULT false,
  enrolled_by text,
  ended_by text,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, worker_id) REFERENCES worker (tenant_id, id),
  FOREIGN KEY (tenant_id, plan_id, option_code) REFERENCES benefit_plan_option (tenant_id, plan_id, code),
  FOREIGN KEY (tenant_id, worker_id, life_event_id) REFERENCES benefit_life_event (tenant_id, worker_id, id),
  CHECK (valid_to IS NULL OR valid_to > valid_from),
  CHECK ((valid_to IS NULL) = (end_reason IS NULL)),
  CONSTRAINT benefit_enrollment_no_overlap EXCLUDE USING gist (
    tenant_id WITH =, worker_id WITH =, plan_id WITH =, validity WITH &&
  )
);
CREATE INDEX IF NOT EXISTS benefit_enrollment_worker ON benefit_enrollment (tenant_id, worker_id, valid_from);

ALTER TABLE benefit_plan ENABLE ROW LEVEL SECURITY;
ALTER TABLE benefit_plan FORCE ROW LEVEL SECURITY;
CREATE POLICY benefit_plan_tenant_access ON benefit_plan AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE benefit_plan_option ENABLE ROW LEVEL SECURITY;
ALTER TABLE benefit_plan_option FORCE ROW LEVEL SECURITY;
CREATE POLICY benefit_plan_option_tenant_access ON benefit_plan_option AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE benefit_life_event ENABLE ROW LEVEL SECURITY;
ALTER TABLE benefit_life_event FORCE ROW LEVEL SECURITY;
CREATE POLICY benefit_life_event_tenant_access ON benefit_life_event AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE benefit_enrollment ENABLE ROW LEVEL SECURITY;
ALTER TABLE benefit_enrollment FORCE ROW LEVEL SECURITY;
CREATE POLICY benefit_enrollment_tenant_access ON benefit_enrollment AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
