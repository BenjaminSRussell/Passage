CREATE TABLE IF NOT EXISTS fiscal_calendar (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,9}$'),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  pattern text NOT NULL CHECK (pattern IN ('calendar', '4-4-5', '4-5-4', '5-4-4', '13-period', 'custom')),
  start_month smallint NOT NULL DEFAULT 1 CHECK (start_month BETWEEN 1 AND 12),
  week_start smallint NOT NULL DEFAULT 1 CHECK (week_start BETWEEN 0 AND 6),
  opening_closing boolean NOT NULL DEFAULT true,
  special_periods smallint NOT NULL DEFAULT 0 CHECK (special_periods BETWEEN 0 AND 4),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  version integer NOT NULL DEFAULT 1,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code),
  UNIQUE (tenant_id, idempotency_key)
);
ALTER TABLE fiscal_calendar ENABLE ROW LEVEL SECURITY;
ALTER TABLE fiscal_calendar FORCE ROW LEVEL SECURITY;
CREATE POLICY fiscal_calendar_tenant_access ON fiscal_calendar AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS fiscal_period (
  tenant_id uuid NOT NULL,
  calendar_id uuid NOT NULL,
  fiscal_year integer NOT NULL CHECK (fiscal_year BETWEEN 1900 AND 2999),
  period_no smallint NOT NULL CHECK (period_no BETWEEN 0 AND 28 OR period_no = 99),
  name text NOT NULL,
  start_date date NOT NULL,
  end_date date NOT NULL,
  type text NOT NULL CHECK (type IN ('opening', 'operating', 'closing', 'adjustment')),
  week53 boolean NOT NULL DEFAULT false,
  generated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, calendar_id, fiscal_year, period_no),
  CHECK (end_date >= start_date),
  FOREIGN KEY (tenant_id, calendar_id) REFERENCES fiscal_calendar (tenant_id, id)
);
ALTER TABLE fiscal_period ENABLE ROW LEVEL SECURITY;
ALTER TABLE fiscal_period FORCE ROW LEVEL SECURITY;
CREATE POLICY fiscal_period_tenant_access ON fiscal_period AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
