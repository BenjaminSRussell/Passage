CREATE SEQUENCE IF NOT EXISTS work_order_number_seq;
CREATE SEQUENCE IF NOT EXISTS asset_number_seq;

CREATE TABLE IF NOT EXISTS asset (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  asset_number text NOT NULL CHECK (char_length(asset_number) BETWEEN 1 AND 30),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  asset_type text NOT NULL CHECK (asset_type ~ '^[A-Z0-9_]{2,20}$'),
  site_id uuid NOT NULL,
  criticality smallint NOT NULL DEFAULT 3 CHECK (criticality BETWEEN 1 AND 5),
  lifecycle_state text NOT NULL DEFAULT 'CREATED' CHECK (lifecycle_state IN ('CREATED', 'ACTIVE', 'INACTIVE', 'DISPOSED')),
  production_resource_id uuid,
  version integer NOT NULL DEFAULT 1,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, asset_number),
  UNIQUE (tenant_id, idempotency_key)
);

CREATE TABLE IF NOT EXISTS counter (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  asset_id uuid NOT NULL,
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 60),
  unit text NOT NULL CHECK (char_length(unit) BETWEEN 1 AND 12),
  cumulative boolean NOT NULL DEFAULT true,
  source text NOT NULL DEFAULT 'MANUAL' CHECK (source IN ('MANUAL', 'IOT')),
  last_reading numeric(14,2) NOT NULL DEFAULT 0,
  last_reading_at timestamptz,
  avg_daily_usage numeric(12,4) NOT NULL DEFAULT 0 CHECK (avg_daily_usage >= 0),
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, asset_id, name),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, asset_id) REFERENCES asset (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS counter_reading (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  counter_id uuid NOT NULL,
  reading_at timestamptz NOT NULL,
  value numeric(14,2) NOT NULL,
  reset boolean NOT NULL DEFAULT false,
  source text NOT NULL CHECK (source IN ('MANUAL', 'IOT')),
  message_id text,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  UNIQUE (tenant_id, counter_id, message_id),
  FOREIGN KEY (tenant_id, counter_id) REFERENCES counter (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS counter_reading_at ON counter_reading (tenant_id, counter_id, reading_at);

CREATE TABLE IF NOT EXISTS maintenance_plan (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{1,29}$'),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  currency char(3) NOT NULL DEFAULT 'USD' CHECK (currency ~ '^[A-Z]{3}$'),
  active boolean NOT NULL DEFAULT true,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code),
  UNIQUE (tenant_id, idempotency_key)
);

CREATE TABLE IF NOT EXISTS plan_line (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  plan_id uuid NOT NULL,
  asset_id uuid NOT NULL,
  line_type text NOT NULL CHECK (line_type IN ('TIME', 'COUNTER', 'TIME_OR_COUNTER', 'TIME_AND_COUNTER')),
  interval_value integer CHECK (interval_value IS NULL OR interval_value >= 1),
  interval_unit text CHECK (interval_unit IS NULL OR interval_unit IN ('DAYS', 'WEEKS', 'MONTHS', 'YEARS')),
  counter_id uuid,
  counter_interval numeric(14,2) CHECK (counter_interval IS NULL OR counter_interval > 0),
  job_type text NOT NULL CHECK (job_type ~ '^[A-Z0-9_]{2,20}$'),
  early_tolerance_pct smallint NOT NULL DEFAULT 10 CHECK (early_tolerance_pct BETWEEN 0 AND 100),
  late_tolerance_pct smallint NOT NULL DEFAULT 10 CHECK (late_tolerance_pct BETWEEN 0 AND 100),
  horizon_days integer NOT NULL DEFAULT 90 CHECK (horizon_days BETWEEN 1 AND 730),
  start_at timestamptz NOT NULL,
  last_completed_at timestamptz,
  last_completed_reading numeric(14,2) NOT NULL DEFAULT 0,
  next_due_at timestamptz,
  next_due_reading numeric(14,2),
  next_due_forecast_at timestamptz,
  active boolean NOT NULL DEFAULT true,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, plan_id) REFERENCES maintenance_plan (tenant_id, id),
  FOREIGN KEY (tenant_id, asset_id) REFERENCES asset (tenant_id, id),
  FOREIGN KEY (tenant_id, counter_id) REFERENCES counter (tenant_id, id),
  CHECK (line_type = 'COUNTER' OR (interval_value IS NOT NULL AND interval_unit IS NOT NULL)),
  CHECK (line_type = 'TIME' OR (counter_id IS NOT NULL AND counter_interval IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS plan_line_next_due ON plan_line (tenant_id, next_due_at);

CREATE TABLE IF NOT EXISTS work_order (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  number text NOT NULL,
  asset_id uuid NOT NULL,
  work_order_type text NOT NULL CHECK (work_order_type IN ('PREVENTIVE', 'CORRECTIVE', 'INSPECTION', 'EMERGENCY', 'CAPITAL', 'ROUND', 'REFURBISHMENT')),
  status text NOT NULL CHECK (status IN ('CREATED', 'SCHEDULED', 'RELEASED', 'IN_PROGRESS', 'ON_HOLD', 'COMPLETED', 'CLOSED', 'CANCELED')),
  priority smallint NOT NULL CHECK (priority BETWEEN 1 AND 5),
  plan_line_id uuid,
  request_id uuid,
  job_type text CHECK (job_type IS NULL OR job_type ~ '^[A-Z0-9_]{2,20}$'),
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  due_at timestamptz,
  actual_start timestamptz,
  actual_end timestamptz,
  downtime_minutes integer NOT NULL DEFAULT 0 CHECK (downtime_minutes >= 0),
  parts_cost numeric(20,6) NOT NULL DEFAULT 0,
  labor_cost numeric(20,6) NOT NULL DEFAULT 0,
  version integer NOT NULL DEFAULT 1,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, number),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, asset_id) REFERENCES asset (tenant_id, id),
  FOREIGN KEY (tenant_id, plan_line_id) REFERENCES plan_line (tenant_id, id),
  CHECK (status NOT IN ('COMPLETED', 'CLOSED') OR (actual_start IS NOT NULL AND actual_end IS NOT NULL AND actual_start <= actual_end))
);
-- One open work order per plan line (scheduler idempotency, BR-44.1 (d)).
CREATE UNIQUE INDEX IF NOT EXISTS work_order_open_plan_line ON work_order (tenant_id, plan_line_id)
  WHERE plan_line_id IS NOT NULL AND status NOT IN ('COMPLETED', 'CLOSED', 'CANCELED');
CREATE INDEX IF NOT EXISTS work_order_status_due ON work_order (tenant_id, status, due_at);
CREATE INDEX IF NOT EXISTS work_order_asset ON work_order (tenant_id, asset_id, actual_end);

-- Append-only status history (BR-44.3); the key makes a retried transition replay.
CREATE TABLE IF NOT EXISTS work_order_transition (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  work_order_id uuid NOT NULL,
  from_status text NOT NULL,
  to_status text NOT NULL,
  at timestamptz NOT NULL,
  actor text,
  idempotency_key text,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, work_order_id) REFERENCES work_order (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS work_order_transition_wo ON work_order_transition (tenant_id, work_order_id, at);

CREATE TABLE IF NOT EXISTS work_order_labor (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  work_order_id uuid NOT NULL,
  hours numeric(9,2) NOT NULL CHECK (hours > 0),
  rate numeric(20,6) NOT NULL CHECK (rate >= 0),
  cost numeric(20,6) NOT NULL,
  worker_id uuid,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, work_order_id) REFERENCES work_order (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS work_order_labor_wo ON work_order_labor (tenant_id, work_order_id);

CREATE TABLE IF NOT EXISTS work_order_part (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  work_order_id uuid NOT NULL,
  item_code text NOT NULL CHECK (char_length(item_code) BETWEEN 1 AND 40),
  qty numeric(14,3) NOT NULL CHECK (qty > 0),
  unit_cost numeric(20,6) NOT NULL CHECK (unit_cost >= 0),
  cost numeric(20,6) NOT NULL,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, work_order_id) REFERENCES work_order (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS maintenance_request (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  asset_id uuid NOT NULL,
  source text NOT NULL CHECK (source IN ('USER', 'MOBILE', 'IOT', 'QUALITY', 'ROUND')),
  severity smallint NOT NULL CHECK (severity BETWEEN 1 AND 5),
  description text NOT NULL CHECK (char_length(description) BETWEEN 1 AND 1000),
  reading_value numeric(14,2),
  status text NOT NULL DEFAULT 'NEW' CHECK (status IN ('NEW', 'TRIAGED', 'CONVERTED', 'REJECTED', 'DEFERRED')),
  work_order_id uuid,
  dedupe_key text,
  condition_rule_id uuid,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, asset_id) REFERENCES asset (tenant_id, id),
  FOREIGN KEY (tenant_id, work_order_id) REFERENCES work_order (tenant_id, id)
);
-- One open request per (asset, rule) (invariant of 8.44.4).
CREATE UNIQUE INDEX IF NOT EXISTS maintenance_request_open_dedupe ON maintenance_request (tenant_id, dedupe_key)
  WHERE dedupe_key IS NOT NULL AND status IN ('NEW', 'TRIAGED');

CREATE TABLE IF NOT EXISTS condition_rule (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  counter_id uuid NOT NULL,
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  operator text NOT NULL CHECK (operator IN ('>', '<')),
  threshold numeric(14,2) NOT NULL,
  severity smallint NOT NULL CHECK (severity BETWEEN 1 AND 5),
  cooldown_minutes integer NOT NULL DEFAULT 60 CHECK (cooldown_minutes BETWEEN 1 AND 1440),
  active boolean NOT NULL DEFAULT true,
  last_fired_at timestamptz,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, counter_id) REFERENCES counter (tenant_id, id)
);

-- Row-level security: tenant_access on every tenant table (dbx.RLSPolicy shape).
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['asset', 'counter', 'counter_reading', 'maintenance_plan', 'plan_line', 'work_order',
                           'work_order_transition', 'work_order_labor', 'work_order_part', 'maintenance_request', 'condition_rule'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I_tenant_access ON %I AS PERMISSIVE
      USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)
      WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)', t, t);
  END LOOP;
END $$;
