-- Absence plans (FR-42.6). Quantities are days or hours, so numeric(12,4).
CREATE TABLE IF NOT EXISTS absence_plan (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL,
  unit text NOT NULL CHECK (unit IN ('HOURS', 'DAYS')),
  accrual_frequency text NOT NULL CHECK (accrual_frequency IN ('MONTHLY', 'BIWEEKLY', 'ANNUAL', 'PER_HOUR_WORKED')),
  accrual_rate numeric(8, 4) NOT NULL CHECK (accrual_rate >= 0),
  accrual_day text NOT NULL CHECK (accrual_day IN ('LAST_DAY', 'FIRST_DAY', 'PAYDATE')),
  proration text NOT NULL CHECK (proration IN ('DAYS_IN_PERIOD', 'NONE')),
  max_balance numeric(12, 4) NOT NULL CHECK (max_balance >= 0),
  carry_over_max numeric(12, 4) NOT NULL DEFAULT 0 CHECK (carry_over_max >= 0),
  carry_over_expiry_months int NOT NULL DEFAULT 3 CHECK (carry_over_expiry_months BETWEEN 0 AND 36),
  plan_year_start text NOT NULL DEFAULT '01-01' CHECK (plan_year_start ~ '^(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01])$'),
  negative_balance_allowed boolean NOT NULL DEFAULT false,
  min_service_days int NOT NULL DEFAULT 0 CHECK (min_service_days >= 0),
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code),
  UNIQUE (tenant_id, idempotency_key)
);

-- Balance per worker, plan, plan year and bucket (BR-42.3, BR-42.4).
-- period_start is the first day of the plan year the bucket belongs to; the
-- balance is derived: accrued - used + adjusted - expired.
CREATE TABLE IF NOT EXISTS absence_balance (
  tenant_id uuid NOT NULL,
  worker_id uuid NOT NULL,
  plan_id uuid NOT NULL,
  period_start date NOT NULL,
  bucket text NOT NULL CHECK (bucket IN ('CURRENT', 'CARRY_OVER')),
  accrued numeric(12, 4) NOT NULL DEFAULT 0,
  used numeric(12, 4) NOT NULL DEFAULT 0,
  adjusted numeric(12, 4) NOT NULL DEFAULT 0,
  expired numeric(12, 4) NOT NULL DEFAULT 0,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, worker_id, plan_id, period_start, bucket),
  FOREIGN KEY (tenant_id, worker_id) REFERENCES worker (tenant_id, id),
  FOREIGN KEY (tenant_id, plan_id) REFERENCES absence_plan (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS absence_balance_plan_year ON absence_balance (tenant_id, plan_id, period_start);

-- One accrual period per (plan, start). The exclusion constraint refuses a
-- second, overlapping period for the same plan, so no day accrues twice.
CREATE TABLE IF NOT EXISTS absence_accrual_period (
  tenant_id uuid NOT NULL,
  plan_id uuid NOT NULL,
  period_start date NOT NULL,
  period_end date NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, plan_id, period_start),
  FOREIGN KEY (tenant_id, plan_id) REFERENCES absence_plan (tenant_id, id),
  CHECK (period_end >= period_start),
  CONSTRAINT absence_accrual_period_no_overlap EXCLUDE USING gist (
    tenant_id WITH =, plan_id WITH =, daterange(period_start, period_end, '[]') WITH &&
  )
);

-- One accrual line per (plan, period, worker): the idempotency key of the
-- hr-accrue job (8.42.16). A rerun credits only workers without a line, so a
-- retried or scope-limited run never accrues anyone twice.
CREATE TABLE IF NOT EXISTS absence_accrual_line (
  tenant_id uuid NOT NULL,
  plan_id uuid NOT NULL,
  period_start date NOT NULL,
  worker_id uuid NOT NULL,
  plan_year_start date NOT NULL,
  employed_days int NOT NULL CHECK (employed_days > 0),
  amount numeric(12, 4) NOT NULL CHECK (amount >= 0),
  discarded numeric(12, 4) NOT NULL DEFAULT 0 CHECK (discarded >= 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, plan_id, period_start, worker_id),
  FOREIGN KEY (tenant_id, plan_id, period_start) REFERENCES absence_accrual_period (tenant_id, plan_id, period_start),
  FOREIGN KEY (tenant_id, worker_id) REFERENCES worker (tenant_id, id)
);

-- Year-end carry-over and carry-over expiry, one line per (plan, plan year,
-- kind, worker): the idempotency key of hr-carryover-expiry (8.42.16).
CREATE TABLE IF NOT EXISTS absence_close_line (
  tenant_id uuid NOT NULL,
  plan_id uuid NOT NULL,
  plan_year_start date NOT NULL,
  kind text NOT NULL CHECK (kind IN ('YEAR_END', 'CARRY_EXPIRY')),
  worker_id uuid NOT NULL,
  carried numeric(12, 4) NOT NULL DEFAULT 0,
  expired numeric(12, 4) NOT NULL DEFAULT 0 CHECK (expired >= 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, plan_id, plan_year_start, kind, worker_id),
  FOREIGN KEY (tenant_id, plan_id) REFERENCES absence_plan (tenant_id, id),
  FOREIGN KEY (tenant_id, worker_id) REFERENCES worker (tenant_id, id)
);

-- Absence request (FR-42.7). DRAFT is not stored in this slice: a request is
-- validated and submitted in one call. decided_by/decided_at record the
-- approver, or the user who cancelled.
CREATE TABLE IF NOT EXISTS absence_request (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  worker_id uuid NOT NULL,
  plan_id uuid NOT NULL,
  date_from date NOT NULL,
  date_to date NOT NULL,
  quantity numeric(12, 4) NOT NULL CHECK (quantity > 0),
  status text NOT NULL CHECK (status IN ('SUBMITTED', 'APPROVED', 'REJECTED', 'CANCELED')),
  reason text,
  requested_by text,
  decided_by text,
  decided_at timestamptz,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, worker_id) REFERENCES worker (tenant_id, id),
  FOREIGN KEY (tenant_id, plan_id) REFERENCES absence_plan (tenant_id, id),
  CHECK (date_to >= date_from),
  CHECK (status = 'SUBMITTED' OR decided_at IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS absence_request_worker ON absence_request (tenant_id, worker_id, date_from, date_to);
CREATE INDEX IF NOT EXISTS absence_request_queue ON absence_request (tenant_id, status, date_from, id);

ALTER TABLE absence_plan ENABLE ROW LEVEL SECURITY;
ALTER TABLE absence_plan FORCE ROW LEVEL SECURITY;
CREATE POLICY absence_plan_tenant_access ON absence_plan AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE absence_balance ENABLE ROW LEVEL SECURITY;
ALTER TABLE absence_balance FORCE ROW LEVEL SECURITY;
CREATE POLICY absence_balance_tenant_access ON absence_balance AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE absence_accrual_period ENABLE ROW LEVEL SECURITY;
ALTER TABLE absence_accrual_period FORCE ROW LEVEL SECURITY;
CREATE POLICY absence_accrual_period_tenant_access ON absence_accrual_period AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE absence_accrual_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE absence_accrual_line FORCE ROW LEVEL SECURITY;
CREATE POLICY absence_accrual_line_tenant_access ON absence_accrual_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE absence_close_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE absence_close_line FORCE ROW LEVEL SECURITY;
CREATE POLICY absence_close_line_tenant_access ON absence_close_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE absence_request ENABLE ROW LEVEL SECURITY;
ALTER TABLE absence_request FORCE ROW LEVEL SECURITY;
CREATE POLICY absence_request_tenant_access ON absence_request AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
