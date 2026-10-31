-- PSG-25 slice 9: supervisor corrections with a reason and pay revision with reversal
-- (FR-25.8, BR-25.8, AC-25.8).
--
-- registration_correction keeps the before and after of every supervisor edit of a
-- registration with the reason, so the audit trail survives the edit.
--
-- An approved worker-day is still immutable: its pay lines are never updated or
-- deleted. A revision adds reversal lines (negative hours and amount, reversal_of
-- pointing at the line they cancel) and new lines, both stamped with the day's new
-- revision number, so the sum of the day's lines is always the current pay.

CREATE TABLE IF NOT EXISTS registration_correction (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  registration_id uuid NOT NULL,
  worker_id uuid NOT NULL,
  reason text NOT NULL CHECK (char_length(reason) BETWEEN 1 AND 500),
  old_started_at timestamptz NOT NULL,
  old_stopped_at timestamptz,
  old_hours numeric(10,4),
  new_started_at timestamptz NOT NULL,
  new_stopped_at timestamptz,
  new_hours numeric(10,4),
  corrected_by text NOT NULL CHECK (char_length(corrected_by) BETWEEN 1 AND 200),
  corrected_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, registration_id) REFERENCES time_registration (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS registration_correction_registration ON registration_correction (tenant_id, registration_id, corrected_at);
CREATE INDEX IF NOT EXISTS registration_correction_worker ON registration_correction (tenant_id, worker_id, corrected_at);
ALTER TABLE registration_correction ENABLE ROW LEVEL SECURITY;
ALTER TABLE registration_correction FORCE ROW LEVEL SECURITY;
CREATE POLICY registration_correction_tenant_access ON registration_correction AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE worker_day ADD COLUMN IF NOT EXISTS revision integer NOT NULL DEFAULT 0;
ALTER TABLE worker_day ADD COLUMN IF NOT EXISTS revised_by text;
ALTER TABLE worker_day ADD COLUMN IF NOT EXISTS revised_at timestamptz;
ALTER TABLE worker_day ADD COLUMN IF NOT EXISTS revision_reason text;

ALTER TABLE pay_line ADD COLUMN IF NOT EXISTS revision integer NOT NULL DEFAULT 0;
ALTER TABLE pay_line ADD COLUMN IF NOT EXISTS reversal_of uuid;
ALTER TABLE pay_line DROP CONSTRAINT IF EXISTS pay_line_hours_check;
ALTER TABLE pay_line ADD CONSTRAINT pay_line_hours_check
  CHECK ((reversal_of IS NULL AND hours > 0) OR (reversal_of IS NOT NULL AND hours < 0));
ALTER TABLE pay_line ADD CONSTRAINT pay_line_reversal_of_fk
  FOREIGN KEY (tenant_id, reversal_of) REFERENCES pay_line (tenant_id, id);
-- A line is reversed at most once.
CREATE UNIQUE INDEX IF NOT EXISTS pay_line_reversal_once ON pay_line (tenant_id, reversal_of) WHERE reversal_of IS NOT NULL;

-- Approved pay lines stay immutable; a revision may only add lines (revision > 0).
CREATE OR REPLACE FUNCTION shopfloor_pay_line_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  t uuid;
  w uuid;
  d date;
BEGIN
  IF TG_OP = 'DELETE' THEN
    t := OLD.tenant_id; w := OLD.worker_id; d := OLD.work_date;
  ELSE
    t := NEW.tenant_id; w := NEW.worker_id; d := NEW.work_date;
  END IF;
  IF EXISTS (SELECT 1 FROM worker_day
             WHERE tenant_id = t AND worker_id = w AND work_date = d AND status = 'approved') THEN
    IF TG_OP = 'INSERT' AND NEW.revision > 0 THEN
      RETURN NEW;
    END IF;
    RAISE EXCEPTION 'approved pay lines are immutable' USING ERRCODE = 'check_violation';
  END IF;
  IF TG_OP = 'DELETE' THEN
    RETURN OLD;
  END IF;
  RETURN NEW;
END $$;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON registration_correction TO passage_app';
  END IF;
END
$$;
