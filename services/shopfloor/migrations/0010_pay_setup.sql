-- PSG-25 slice 11: persisted time profiles and pay agreements, and the link from a
-- worker (FR-25.7, BR-25.2, BR-25.8; PRD 4.25.4 ProfileDay and pay agreement).
--
-- A time profile holds the day rules of one working pattern: a profile_day row per day
-- type (weekday, saturday, sunday) with shift window, unpaid breaks, rounding and
-- tolerances. A pay agreement holds the overtime thresholds and multipliers and the pay
-- week start. A worker without a profile or agreement is calculated with the defaults
-- (10 min before and 5 min after tolerance, 8 h daily and 40 h weekly at 1.5x, Sunday 2.0x).
-- Both are versioned: an update carries the version it read (If-Match) and bumps it.

CREATE TABLE IF NOT EXISTS time_profile (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (char_length(code) BETWEEN 1 AND 40),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  active boolean NOT NULL DEFAULT true,
  version integer NOT NULL DEFAULT 1,
  created_by text NOT NULL CHECK (char_length(created_by) BETWEEN 1 AND 200),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code)
);
ALTER TABLE time_profile ENABLE ROW LEVEL SECURITY;
ALTER TABLE time_profile FORCE ROW LEVEL SECURITY;
CREATE POLICY time_profile_tenant_access ON time_profile AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS profile_day (
  tenant_id uuid NOT NULL,
  profile_id uuid NOT NULL,
  day_type text NOT NULL CHECK (day_type IN ('weekday', 'saturday', 'sunday')),
  shift_start time,
  shift_end time,
  break_windows jsonb NOT NULL DEFAULT '[]',
  rounding_minutes integer NOT NULL DEFAULT 1 CHECK (rounding_minutes BETWEEN 1 AND 15),
  tolerance_before_min integer NOT NULL DEFAULT 10 CHECK (tolerance_before_min BETWEEN 0 AND 60),
  tolerance_after_min integer NOT NULL DEFAULT 5 CHECK (tolerance_after_min BETWEEN 0 AND 60),
  flex_allowed boolean NOT NULL DEFAULT false,
  PRIMARY KEY (tenant_id, profile_id, day_type),
  FOREIGN KEY (tenant_id, profile_id) REFERENCES time_profile (tenant_id, id),
  CHECK ((shift_start IS NULL) = (shift_end IS NULL)),
  CHECK (shift_start IS NULL OR shift_end > shift_start)
);
ALTER TABLE profile_day ENABLE ROW LEVEL SECURITY;
ALTER TABLE profile_day FORCE ROW LEVEL SECURITY;
CREATE POLICY profile_day_tenant_access ON profile_day AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS pay_agreement (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (char_length(code) BETWEEN 1 AND 40),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  daily_threshold numeric(6,2) NOT NULL CHECK (daily_threshold BETWEEN 0 AND 24),
  weekly_threshold numeric(6,2) NOT NULL CHECK (weekly_threshold BETWEEN 0 AND 168),
  overtime_multiplier numeric(6,4) NOT NULL CHECK (overtime_multiplier BETWEEN 1 AND 5),
  sunday_multiplier numeric(6,4) NOT NULL CHECK (sunday_multiplier BETWEEN 1 AND 5),
  week_start smallint NOT NULL CHECK (week_start BETWEEN 0 AND 6),
  active boolean NOT NULL DEFAULT true,
  version integer NOT NULL DEFAULT 1,
  created_by text NOT NULL CHECK (char_length(created_by) BETWEEN 1 AND 200),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code)
);
ALTER TABLE pay_agreement ENABLE ROW LEVEL SECURITY;
ALTER TABLE pay_agreement FORCE ROW LEVEL SECURITY;
CREATE POLICY pay_agreement_tenant_access ON pay_agreement AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE worker ADD COLUMN IF NOT EXISTS profile_id uuid;
ALTER TABLE worker ADD COLUMN IF NOT EXISTS pay_agreement_id uuid;
ALTER TABLE worker ADD CONSTRAINT worker_profile_fk FOREIGN KEY (tenant_id, profile_id) REFERENCES time_profile (tenant_id, id);
ALTER TABLE worker ADD CONSTRAINT worker_pay_agreement_fk FOREIGN KEY (tenant_id, pay_agreement_id) REFERENCES pay_agreement (tenant_id, id);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON time_profile, pay_agreement TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON profile_day TO passage_app';
  END IF;
END
$$;
