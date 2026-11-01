-- PSG-25 slice 13: absence codes and absence pay lines (FR-25.2 absence registrations, FR-25.7
-- pay types absence-paid and absence-unpaid). An absence code says whether the time is paid and
-- at what share of the worker's rate. An absence registration (kind absence) names a code and
-- covers a closed interval of at most 24 hours; pay:calculate turns its hours into an
-- absence-paid or absence-unpaid line on the local day it starts, like a worked segment. The
-- code is versioned: an update carries the version it read (If-Match). Its pay terms (paid,
-- pay_multiplier) are fixed once an absence names it, so recalculating a past day never pays
-- it differently.

CREATE TABLE IF NOT EXISTS absence_code (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (char_length(code) BETWEEN 1 AND 40),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  paid boolean NOT NULL,
  -- Share of the worker's cost rate paid per absence hour: above 0 and at most 5 for a paid
  -- code, 0 for an unpaid one.
  pay_multiplier numeric(6,4) NOT NULL,
  active boolean NOT NULL DEFAULT true,
  version integer NOT NULL DEFAULT 1,
  created_by text NOT NULL CHECK (char_length(created_by) BETWEEN 1 AND 200),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code),
  CHECK ((paid AND pay_multiplier > 0 AND pay_multiplier <= 5) OR (NOT paid AND pay_multiplier = 0))
);
ALTER TABLE absence_code ENABLE ROW LEVEL SECURITY;
ALTER TABLE absence_code FORCE ROW LEVEL SECURITY;
CREATE POLICY absence_code_tenant_access ON absence_code AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE time_registration ADD COLUMN IF NOT EXISTS absence_code_id uuid;
ALTER TABLE time_registration ADD CONSTRAINT time_registration_absence_fk
  FOREIGN KEY (tenant_id, absence_code_id) REFERENCES absence_code (tenant_id, id);
ALTER TABLE time_registration ADD CONSTRAINT time_registration_absence_kind
  CHECK (absence_code_id IS NULL OR kind = 'absence');

-- An absence-unpaid line has multiplier 0 (hours x rate x 0 = amount 0); every other line keeps
-- a positive multiplier.
ALTER TABLE pay_line DROP CONSTRAINT IF EXISTS pay_line_rate_multiplier_check;
ALTER TABLE pay_line ADD CONSTRAINT pay_line_rate_multiplier_check
  CHECK (rate_multiplier > 0 OR (pay_type = 'absence-unpaid' AND rate_multiplier = 0));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON absence_code TO passage_app';
  END IF;
END
$$;
